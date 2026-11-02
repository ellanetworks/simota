package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
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
	PutSMSC(ctx context.Context, s db.SMSC) (db.SMSC, error)
}

type Config struct {
	Store          Store
	Frontend       fs.FS
	Version        string
	Notify         func()
	Ready          func(ctx context.Context) error
	Gateway        Gateway
	TracerProvider trace.TracerProvider
	Metrics        prometheus.Gatherer
	Requests       *RequestMetrics
	Logger         *slog.Logger
}

type Gateway interface {
	Apply(ctx context.Context, s *gateway.Settings) error
	State() gateway.State
}

const (
	maxImportBody = 16 << 20
	maxBody       = 64 << 10

	defaultPerPage = 25
	maxPerPage     = 100
)

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.Handler) {
		mux.Handle(pattern, cfg.Requests.instrument(pattern, h))
	}

	handle("GET /api/v1/livez", Livez())
	handle("GET /api/v1/readyz", Readyz(cfg))
	handle("GET /api/v1/status", GetStatus(cfg))
	handle("GET /api/v1/metrics", GetMetrics(cfg))
	handle("GET /api/v1/openapi.yaml", OpenAPISpec())
	handle("GET /api/v1/cards", ListCards(cfg))
	handle("POST /api/v1/cards", CreateCard(cfg))
	handle("POST /api/v1/cards/import", ImportCards(cfg))
	handle("GET /api/v1/cards/{iccid}", GetCard(cfg))
	handle("PUT /api/v1/cards/{iccid}", UpdateCard(cfg))
	handle("DELETE /api/v1/cards/{iccid}", DeleteCard(cfg))
	handle("PUT /api/v1/cards/{iccid}/key-sets/{version}", UpdateKeySet(cfg))
	handle("POST /api/v1/jobs", CreateJobs(cfg))
	handle("GET /api/v1/jobs", ListJobs(cfg))
	handle("GET /api/v1/jobs/{id}", GetJob(cfg))
	handle("GET /api/v1/smsc", GetSMSC(cfg))
	handle("PUT /api/v1/smsc", UpdateSMSC(cfg))
	handle("GET /api/v1/pprof/{$}", http.HandlerFunc(pprof.Index))
	handle("GET /api/v1/pprof/allocs", pprof.Handler("allocs"))
	handle("GET /api/v1/pprof/goroutine", pprof.Handler("goroutine"))
	handle("GET /api/v1/pprof/goroutineleak", pprof.Handler("goroutineleak"))
	handle("GET /api/v1/pprof/heap", pprof.Handler("heap"))
	handle("GET /api/v1/pprof/threadcreate", pprof.Handler("threadcreate"))
	handle("GET /api/v1/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	handle("GET /api/v1/pprof/profile", http.HandlerFunc(pprof.Profile))
	handle("GET /api/v1/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	handle("POST /api/v1/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	handle("GET /api/v1/pprof/trace", http.HandlerFunc(pprof.Trace))

	if cfg.Frontend != nil {
		handle("GET /", Frontend(cfg.Frontend))
	}

	return traced(cfg.TracerProvider, mux)
}

func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func Readyz(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Ready != nil {
			if err := cfg.Ready(r.Context()); err != nil {
				cfg.Logger.WarnContext(r.Context(), "not ready", slog.Any("error", err))
				writeJSON(w, r, ErrorResponse{Error: "Not ready"}, http.StatusServiceUnavailable, cfg.Logger)

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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(w, r, Status{Version: cfg.Version}, http.StatusOK, cfg.Logger)
	})
}

type Response struct {
	Result any `json:"result,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type SuccessResponse struct {
	Message string `json:"message"`
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any, cfg Config) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
		return false
	}

	return true
}

func pagination(w http.ResponseWriter, r *http.Request, cfg Config) (int, int, bool) {
	q := r.URL.Query()

	page, ok := atoiDefault(q.Get("page"), 1)
	if !ok || page < 1 {
		writeError(w, r, http.StatusBadRequest, "page must be an integer >= 1", nil, cfg.Logger)
		return 0, 0, false
	}

	perPage, ok := atoiDefault(q.Get("per_page"), defaultPerPage)
	if !ok || perPage < 1 || perPage > maxPerPage {
		writeError(w, r, http.StatusBadRequest, "per_page must be an integer between 1 and 100", nil, cfg.Logger)
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

func writeResponse(w http.ResponseWriter, r *http.Request, v any, status int, logger *slog.Logger) {
	writeJSON(w, r, Response{Result: v}, status, logger)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string, err error, logger *slog.Logger) {
	if status >= 500 {
		logger.ErrorContext(r.Context(), message, slog.Any("error", err))
	} else {
		logger.DebugContext(r.Context(), message, slog.Any("error", err))
	}

	writeJSON(w, r, ErrorResponse{Error: message}, status, logger)
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any, status int, logger *slog.Logger) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.ErrorContext(r.Context(), "failed to encode API response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(b); err != nil {
		logger.DebugContext(r.Context(), "failed to write API response", slog.Any("error", err))
	}
}

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
