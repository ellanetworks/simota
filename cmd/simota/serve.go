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
	"github.com/ellanetworks/simota/ui"
)

const version = "v0.0.1"

// serve runs the API and the web UI, as the configuration file says.
func serve(args []string) error {
	fs := flag.NewFlagSet("simota serve", flag.ContinueOnError)
	configPath := fs.String("config", "simota.yaml", "path to the configuration file")

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Logging.Level)
	addr := netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	defer func() { _ = store.Close() }()

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewHandler(api.Config{Store: store, Frontend: ui.FS(), Version: version, Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)

	go func() { errc <- srv.ListenAndServe() }()

	logger.Info("simota started", slog.String("addr", addr), slog.String("version", version))

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
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

// cardSettings are the OTA settings of a stored card.
func cardSettings(ctx context.Context, dbPath, iccid string) (db.Card, db.KeySet, error) {
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		return db.Card{}, db.KeySet{}, err
	}

	defer func() { _ = store.Close() }()

	c, err := store.GetCard(ctx, iccid)
	if err != nil {
		return db.Card{}, db.KeySet{}, fmt.Errorf("card %s: %w", iccid, err)
	}

	for _, k := range c.KeySets {
		if k.Version == c.KVN {
			return c, k, nil
		}
	}

	return db.Card{}, db.KeySet{}, fmt.Errorf("card %s has no key set %d", iccid, c.KVN)
}
