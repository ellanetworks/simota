// Package api serves simota's REST API and its web UI.
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/ellanetworks/simota/internal/db"
)

type Store interface {
	CreateCard(ctx context.Context, c db.Card) error
	UpdateCard(ctx context.Context, c db.Card) error
	DeleteCard(ctx context.Context, iccid string) error
	GetCard(ctx context.Context, iccid string) (db.Card, error)
	ListCards(ctx context.Context) ([]db.Card, error)
	ImportCards(ctx context.Context, cards []db.Card) (created, updated int, err error)
}

type Config struct {
	Store    Store
	Frontend fs.FS
	Version  string
	Logger   *slog.Logger
}

// maxBody is the largest request body: a key file of some thousands of cards.
const maxBody = 16 << 20

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /api/v1/status", GetStatus(cfg))
	mux.Handle("GET /api/v1/cards", ListCards(cfg))
	mux.Handle("POST /api/v1/cards", CreateCard(cfg))
	mux.Handle("POST /api/v1/cards/import", ImportCards(cfg))
	mux.Handle("GET /api/v1/cards/{iccid}", GetCard(cfg))
	mux.Handle("PUT /api/v1/cards/{iccid}", UpdateCard(cfg))
	mux.Handle("DELETE /api/v1/cards/{iccid}", DeleteCard(cfg))

	if cfg.Frontend != nil {
		mux.Handle("GET /", Frontend(cfg.Frontend))
	}

	return http.MaxBytesHandler(mux, maxBody)
}

type Status struct {
	Version string `json:"version"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, Status{Version: cfg.Version}, http.StatusOK, cfg.Logger)
	})
}

type Response struct {
	Result any `json:"result,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeResponse(w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	writeJSON(w, Response{Result: v}, status, logger)
}

func writeError(w http.ResponseWriter, status int, message string, err error, logger *slog.Logger) {
	if status >= 500 {
		logger.Error(message, slog.Any("error", err))
	} else {
		logger.Debug(message, slog.Any("error", err))
	}

	writeJSON(w, ErrorResponse{Error: message}, status, logger)
}

func writeJSON(w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.Error("failed to encode API response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(b); err != nil {
		logger.Debug("failed to write API response", slog.Any("error", err))
	}
}

// Frontend serves the web UI: its files, and its index for the other paths outside the API, which the UI routes.
func Frontend(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !exists(fsys, "index.html") {
			http.NotFound(w, r)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)

			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		if !exists(fsys, r.URL.Path) {
			http.ServeFileFS(w, r, fsys, "index.html")
			return
		}

		files.ServeHTTP(w, r)
	})
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, strings.TrimPrefix(path.Clean("/"+name), "/"))
	return err == nil
}
