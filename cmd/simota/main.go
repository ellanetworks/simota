package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/config"
	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
	"github.com/ellanetworks/simota/internal/jobs"
	"github.com/ellanetworks/simota/internal/logging"
	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/simota/ui"
	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/trace"
)

const (
	version = "v0.0.1"

	jobInterval     = 2 * time.Second
	shutdownTimeout = 5 * time.Second
)

func main() {
	configPath := flag.String("config", "simota.yaml", "path to the configuration file")

	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logging.New(os.Stdout, slog.LevelInfo).Error("simota stopped", slog.Any("error", err))
		os.Exit(1)
	}

	logger := logging.New(os.Stdout, cfg.Logging.Level)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err = run(ctx, cfg, logger)

	stop()

	if err != nil {
		logger.Error("simota stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	tracerProvider, shutdownTracing, err := newTracerProvider(ctx, cfg.Telemetry, logger)
	if err != nil {
		return err
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := shutdownTracing(ctx); err != nil {
			logger.WarnContext(ctx, "failed to export the last spans", slog.String("otlp_endpoint", cfg.Telemetry.OTLPEndpoint),
				slog.Any("error", err))
		}
	}()

	store, err := db.Open(ctx, cfg.DB.Path, db.WithTracerProvider(tracerProvider))
	if err != nil {
		return err
	}

	defer func() { _ = store.Close() }()

	var runner *jobs.Runner

	gw := gateway.New(gateway.Config{
		Handler: smpp.HandlerFunc(func(ctx context.Context, s *smpp.Session, req smpp.Request) smpp.Response {
			return runner.ServeSMPP(ctx, s, req)
		}),
		TracerProvider: tracerProvider,
		Logger:         logger,
	})

	runner = jobs.NewRunner(store, func(ctx context.Context) (jobs.Gateway, error) {
		s, err := store.GetSMSC(ctx)
		if errors.Is(err, db.ErrNotFound) {
			return jobs.Gateway{}, jobs.ErrNoGateway
		}

		if err != nil {
			return jobs.Gateway{}, err
		}

		return jobs.Gateway{SMSC: gw, Originator: s.Originator, Validity: s.Validity}, nil
	}, jobInterval, logger)
	runner.TracerProvider = tracerProvider

	switch s, err := store.GetSMSC(ctx); {
	case err == nil:
		if err := gw.Apply(ctx, new(gateway.SettingsOf(s))); err != nil {
			logger.ErrorContext(ctx, "failed to connect to the SMSC", slog.Any("error", err))
		}
	case !errors.Is(err, db.ErrNotFound):
		return err
	}

	addr := netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String()
	srv := &http.Server{
		Addr: addr,
		Handler: api.NewHandler(api.Config{
			Store: store, Frontend: ui.FS(), Version: version, Notify: runner.Wake, Gateway: gw, Ready: store.Ping,
			TracerProvider: tracerProvider, Logger: logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	runnerDone := make(chan struct{})

	go func() {
		runner.Run(ctx)
		close(runnerDone)
	}()

	errc := make(chan error, 1)

	go func() { errc <- srv.ListenAndServe() }()

	logger.InfoContext(ctx, "simota started", slog.String("addr", addr), slog.String("version", version))

	select {
	case err = <-errc:
		err = fmt.Errorf("serve: %w", err)

		stop()
	case <-ctx.Done():
	}

	if serr := shutdownAPI(srv); serr != nil && err == nil {
		err = serr
	}

	<-runnerDone

	gwCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if gerr := gw.Close(gwCtx); gerr != nil && err == nil {
		err = gerr
	}

	return err
}

func newTracerProvider(ctx context.Context, cfg config.Telemetry, logger *slog.Logger) (trace.TracerProvider,
	func(context.Context) error, error,
) {
	if !cfg.Enabled {
		return nil, func(context.Context) error { return nil }, nil
	}

	tp, err := tracing.NewProvider(ctx, cfg.OTLPEndpoint, version, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("start tracing: %w", err)
	}

	return tp, tp.Shutdown, nil
}

func shutdownAPI(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err := srv.Shutdown(ctx)
	if err != nil {
		_ = srv.Close()
	}

	return err
}
