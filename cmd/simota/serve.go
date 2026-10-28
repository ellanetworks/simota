package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/ui"
)

const version = "v0.0.1"

// serve runs the API and the web UI. They have no authentication, so they listen on the loopback interface unless
// told otherwise.
func serve(args []string) error {
	fs := flag.NewFlagSet("simota serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:5020", "address to listen on")
	dbPath := fs.String("db", "simota.db", "path to the database")

	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, *dbPath)
	if err != nil {
		return err
	}

	defer func() { _ = store.Close() }()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewHandler(api.Config{Store: store, Frontend: ui.FS(), Version: version, Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)

	go func() { errc <- srv.ListenAndServe() }()

	logger.Info("simota started", slog.String("addr", *addr), slog.String("version", version))

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
