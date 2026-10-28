// Command simota is a minimal SIM OTA platform: it keeps cards and their OTA keys, and runs Remote File Management
// jobs on them through an SMSC, such as Ella SMSC, as an SMPP 3.4 client.
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
	"strings"
	"syscall"
	"time"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/config"
	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
	"github.com/ellanetworks/simota/internal/jobs"
	"github.com/ellanetworks/simota/ui"
	"github.com/ellanetworks/smsc/smpp"
)

const (
	version = "v0.0.1"

	// jobInterval is how often the runner sends the queued jobs, when not woken sooner.
	jobInterval = 2 * time.Second
	// shutdownTimeout is how long the API has to finish its requests, and the SMPP session to unbind, on stop.
	shutdownTimeout = 5 * time.Second
)

func main() {
	configPath := flag.String("config", "simota.yaml", "path to the configuration file")

	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		newLogger(slog.LevelInfo).Error("simota stopped", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Logging.Level)

	if err := run(cfg, logger); err != nil {
		logger.Error("simota stopped", "error", err)
		os.Exit(1)
	}
}

// newLogger logs JSON as Ella Core, Ella SMSC and Ella IMS do: ts, a lowercase level, and durations as "1.5s".
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch {
			case a.Key == slog.TimeKey:
				a.Key = "ts"
			case a.Key == slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			case a.Value.Kind() == slog.KindDuration:
				a.Value = slog.StringValue(a.Value.Duration().String())
			}

			return a
		},
	}))
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	defer func() { _ = store.Close() }()

	// One SMPP session to the SMSC per process, bound as a transceiver: the runner submits on it and takes the
	// SMSC's deliver_sm from it.
	var runner *jobs.Runner

	gw := gateway.New(gateway.Config{
		Handler: smpp.HandlerFunc(func(ctx context.Context, s *smpp.Session, req smpp.Request) smpp.Response {
			return runner.ServeSMPP(ctx, s, req)
		}),
		Logger: logger,
	})

	// The runner reads the originator and validity as set over the API at each step.
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

	switch s, err := store.GetSMSC(ctx); {
	case err == nil:
		if err := gw.Apply(new(gateway.SettingsOf(s))); err != nil {
			logger.Error("failed to connect to the SMSC", slog.Any("error", err))
		}
	case !errors.Is(err, db.ErrNotFound):
		return err
	}

	addr := netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String()
	srv := &http.Server{
		Addr: addr,
		Handler: api.NewHandler(api.Config{
			Store: store, Frontend: ui.FS(), Version: version, Notify: runner.Wake, Gateway: gw, Logger: logger,
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

	logger.Info("simota started", slog.String("addr", addr), slog.String("version", version))

	select {
	case err = <-errc:
		err = fmt.Errorf("serve: %w", err)

		stop()
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if serr := srv.Shutdown(shutdownCtx); serr != nil && !errors.Is(serr, http.ErrServerClosed) && err == nil {
		err = serr
	}

	<-runnerDone

	if gerr := gw.Close(shutdownCtx); gerr != nil && err == nil {
		err = gerr
	}

	return err
}
