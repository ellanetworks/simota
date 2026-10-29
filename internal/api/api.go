// Package api serves simota's REST API and its web UI.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
)

type Store interface {
	CreateCard(ctx context.Context, c db.Card) error
	UpdateCard(ctx context.Context, c db.Card) error
	DeleteCard(ctx context.Context, iccid string) error
	GetCard(ctx context.Context, iccid string) (db.Card, error)
	ListCards(ctx context.Context, page, perPage int) ([]db.Card, int, error)
	ImportCards(ctx context.Context, cards []db.Card) (created, updated int, err error)
	CreateJobs(ctx context.Context, jobs []db.Job) ([]int64, error)
	GetJob(ctx context.Context, id int64) (db.Job, error)
	ListJobs(ctx context.Context, search string, page, perPage int) ([]db.Job, int, error)
	SetCounter(ctx context.Context, iccid string, version int, counter uint64) error
	GetSMSC(ctx context.Context) (db.SMSC, error)
	PutSMSC(ctx context.Context, s db.SMSC) error
}

type Config struct {
	Store    Store
	Frontend fs.FS
	Version  string
	// Notify, when set, tells the job runner that jobs are queued or the SMSC changed.
	Notify func()
	// Ready, when set, tells whether simota can serve requests: nil, or what is wrong.
	Ready func(ctx context.Context) error
	// Gateway, when set, is the SMPP session to the SMSC: PUT /api/v1/smsc applies its settings, GET shows its state.
	Gateway Gateway
	Logger  *slog.Logger
}

// Gateway is the SMPP session to the SMSC.
type Gateway interface {
	Apply(s *gateway.Settings) error
	State() gateway.State
}

const (
	// maxImportBody bounds a key file, of some thousands of cards.
	maxImportBody = 16 << 20
	// maxBody bounds the other request bodies.
	maxBody = 64 << 10

	defaultPerPage = 25
	maxPerPage     = 100
)

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /api/v1/livez", Livez())
	mux.Handle("GET /api/v1/readyz", Readyz(cfg))
	mux.Handle("GET /api/v1/status", GetStatus(cfg))
	mux.Handle("GET /api/v1/openapi.yaml", OpenAPISpec())
	mux.Handle("GET /api/v1/cards", ListCards(cfg))
	mux.Handle("POST /api/v1/cards", CreateCard(cfg))
	mux.Handle("POST /api/v1/cards/import", ImportCards(cfg))
	mux.Handle("GET /api/v1/cards/{iccid}", GetCard(cfg))
	mux.Handle("PUT /api/v1/cards/{iccid}", UpdateCard(cfg))
	mux.Handle("DELETE /api/v1/cards/{iccid}", DeleteCard(cfg))
	mux.Handle("PUT /api/v1/cards/{iccid}/key-sets/{version}", UpdateKeySet(cfg))
	mux.Handle("POST /api/v1/jobs", CreateJobs(cfg))
	mux.Handle("GET /api/v1/jobs", ListJobs(cfg))
	mux.Handle("GET /api/v1/jobs/{id}", GetJob(cfg))
	mux.Handle("GET /api/v1/smsc", GetSMSC(cfg))
	mux.Handle("PUT /api/v1/smsc", UpdateSMSC(cfg))

	if cfg.Frontend != nil {
		mux.Handle("GET /", Frontend(cfg.Frontend))
	}

	return mux
}

// Livez tells the kubelet that the process serves HTTP. It checks nothing else, so that a failing database fails
// readiness and does not restart simota.
func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// Readyz tells whether simota can serve requests: 204 when cfg.Ready finds nothing wrong, 503 otherwise.
func Readyz(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Ready != nil {
			if err := cfg.Ready(r.Context()); err != nil {
				cfg.Logger.Warn("not ready", slog.Any("error", err))
				writeJSON(w, ErrorResponse{Error: "Not ready"}, http.StatusServiceUnavailable, cfg.Logger)

				return
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

//go:embed openapi.yaml
var openapiSpec []byte

func OpenAPISpec() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/openapi+yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(openapiSpec)
	})
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

// SuccessResponse is the response to a change that returns nothing else.
type SuccessResponse struct {
	Message string `json:"message"`
}

// decodeBody decodes a JSON request body into v, refusing unknown fields, and answers 400 when it cannot.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, cfg Config) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
		return false
	}

	return true
}

func pagination(w http.ResponseWriter, r *http.Request, cfg Config) (int, int, bool) {
	q := r.URL.Query()

	page, ok := atoiDefault(q.Get("page"), 1)
	if !ok || page < 1 {
		writeError(w, http.StatusBadRequest, "page must be an integer >= 1", nil, cfg.Logger)
		return 0, 0, false
	}

	perPage, ok := atoiDefault(q.Get("per_page"), defaultPerPage)
	if !ok || perPage < 1 || perPage > maxPerPage {
		writeError(w, http.StatusBadRequest, "per_page must be an integer between 1 and 100", nil, cfg.Logger)
		return 0, 0, false
	}

	return page, perPage, true
}

func atoiDefault(s string, def int) (int, bool) {
	if s == "" {
		return def, true
	}

	n, err := strconv.Atoi(s)

	return n, err == nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
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
