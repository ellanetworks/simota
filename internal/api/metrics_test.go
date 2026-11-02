package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/prometheus/client_golang/prometheus"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d", rec.Code)
	}

	return rec.Body.String()
}

func TestMetrics(t *testing.T) {
	store := openStore(t)
	registry := prometheus.NewRegistry()
	registry.MustRegister(store.Collectors()...)

	h := api.NewHandler(api.Config{Store: store, Metrics: registry, Logger: slog.New(slog.DiscardHandler)})

	body := scrape(t, h)
	for _, series := range []string{
		`ellasimota_jobs_pending{status="queued"} 0`,
		`ellasimota_jobs_completed_total{operation="imsi_write",status="succeeded"} 0`,
	} {
		if !strings.Contains(body, "\n"+series+"\n") {
			t.Errorf("metrics do not have %q", series)
		}
	}
}

func TestMetricsCountRequestsByRouteAndCode(t *testing.T) {
	requests := api.NewRequestMetrics()
	registry := prometheus.NewRegistry()
	registry.MustRegister(requests.Collectors()...)

	h := api.NewHandler(api.Config{
		Store: openStore(t), Metrics: registry, Requests: requests, Logger: slog.New(slog.DiscardHandler),
		Frontend: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
	})

	for _, r := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodGet, "/api/v1/cards", "", http.StatusOK},
		{http.MethodGet, "/api/v1/cards/8988211000000000017", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/jobs", "{", http.StatusBadRequest},
		{http.MethodGet, "/api/v1/pprof/cmdline", "", http.StatusOK},
		{http.MethodGet, "/", "", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), r.method, r.path, strings.NewReader(r.body)))

		if rec.Code != r.code {
			t.Fatalf("%s %s = %d, want %d", r.method, r.path, rec.Code, r.code)
		}
	}

	body := scrape(t, h)
	for _, series := range []string{
		`ellasimota_api_request_duration_seconds_count{code="200",method="get",route="/api/v1/cards"} 1`,
		`ellasimota_api_request_duration_seconds_count{code="404",method="get",route="/api/v1/cards/{iccid}"} 1`,
		`ellasimota_api_request_duration_seconds_count{code="400",method="post",route="/api/v1/jobs"} 1`,
	} {
		if !strings.Contains(body, "\n"+series+"\n") {
			t.Errorf("metrics do not have %q", series)
		}
	}

	for _, route := range []string{`route="/api/v1/pprof/`, `route="/"`} {
		if strings.Contains(body, route) {
			t.Errorf("metrics have %s", route)
		}
	}
}

func TestMetricsWithoutRegistry(t *testing.T) {
	h := api.NewHandler(api.Config{Store: openStore(t), Logger: slog.New(slog.DiscardHandler)})

	if body := scrape(t, h); body != "" {
		t.Fatalf("metrics without a registry = %q, want none", body)
	}
}
