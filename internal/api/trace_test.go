package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/logging"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const callerTraceParent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

// failingStore is the store, whose cards cannot be listed.
type failingStore struct{ *db.DB }

func (failingStore) ListCards(context.Context, int, int) ([]db.Card, int, error) {
	return nil, 0, errors.New("database is locked")
}

// tracedAPI is the API traced to a span recorder, with its logs in a buffer.
type tracedAPI struct {
	handler http.Handler
	spans   *tracetest.SpanRecorder
	logs    *bytes.Buffer
}

func newTracedAPI(t *testing.T, store api.Store) tracedAPI {
	t.Helper()

	spans := tracetest.NewSpanRecorder()
	logs := &bytes.Buffer{}

	return tracedAPI{
		handler: api.NewHandler(api.Config{
			Store: store, Version: "test", Frontend: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
			TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
			Logger:         logging.New(logs, slog.LevelDebug),
		}),
		spans: spans,
		logs:  logs,
	}
}

func (a tracedAPI) serve(t *testing.T, method, path, traceparent string) int {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}

	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)

	return rec.Code
}

// attr is the value of a span's attribute key, if it has it.
func attr(s sdktrace.ReadOnlySpan, key attribute.Key) (attribute.Value, bool) {
	set := attribute.NewSet(s.Attributes()...)
	return set.Value(key)
}

func openStore(t *testing.T) *db.DB {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	return store
}

// TestRequestSpan checks that a request to the API has a server span named by its route, without a parent but with a
// link to the caller's trace context.
func TestRequestSpan(t *testing.T) {
	a := newTracedAPI(t, openStore(t))

	if code := a.serve(t, http.MethodGet, "/api/v1/cards/8988211000000000017", callerTraceParent); code != http.StatusNotFound {
		t.Fatalf("GET card = %d", code)
	}

	spans := a.spans.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended %d spans, want 1", len(spans))
	}

	s := spans[0]
	if s.Name() != "GET /api/v1/cards/{iccid}" || s.SpanKind() != trace.SpanKindServer || s.Parent().IsValid() {
		t.Fatalf("span %q kind %v parent %v, want a server span named by its route, without a parent", s.Name(),
			s.SpanKind(), s.Parent())
	}

	if route, _ := attr(s, "http.route"); route.AsString() != "/api/v1/cards/{iccid}" {
		t.Errorf("http.route = %q", route.AsString())
	}

	if links := s.Links(); len(links) != 1 || links[0].SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("links = %+v, want one to the caller's trace context", links)
	}

	// A 404 is the client's: the span is not an error.
	if s.Status().Code != codes.Unset {
		t.Errorf("status = %+v", s.Status())
	}

	if _, ok := attr(s, "error.type"); ok {
		t.Errorf("a 404 has an error.type")
	}
}

// TestUntraced checks that the UI's files, the probes and the OpenAPI document have no span.
func TestUntraced(t *testing.T) {
	a := newTracedAPI(t, openStore(t))

	for _, path := range []string{"/", "/cards", "/api/v1/livez", "/api/v1/readyz", "/api/v1/openapi.yaml"} {
		a.serve(t, http.MethodGet, path, "")
	}

	if spans := a.spans.Ended(); len(spans) != 0 {
		t.Fatalf("ended %d spans, first %q", len(spans), spans[0].Name())
	}
}

// TestFailedRequestSpan checks that a request that fails is an error of its span, with error.type its status, and that
// its cause, logged, carries the span's trace context.
func TestFailedRequestSpan(t *testing.T) {
	a := newTracedAPI(t, failingStore{openStore(t)})

	if code := a.serve(t, http.MethodGet, "/api/v1/cards", ""); code != http.StatusInternalServerError {
		t.Fatalf("GET cards = %d", code)
	}

	spans := a.spans.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended %d spans, want 1", len(spans))
	}

	s := spans[0]
	if errType, _ := attr(s, "error.type"); s.Status().Code != codes.Error ||
		errType.AsString() != "500" {
		t.Fatalf("status %+v, error.type %q, want an error of type 500", s.Status(), errType.AsString())
	}

	var line map[string]any
	if err := json.Unmarshal(a.logs.Bytes(), &line); err != nil {
		t.Fatalf("log %q: %v", a.logs.String(), err)
	}

	if line["level"] != "error" || line["error"] != "database is locked" ||
		line[logging.TraceIDKey] != s.SpanContext().TraceID().String() ||
		line[logging.SpanIDKey] != s.SpanContext().SpanID().String() {
		t.Fatalf("log = %v, want the error in span %v", line, s.SpanContext())
	}
}

// TestJobCreationContext checks that the store's operations for a request are children of its span, and that the jobs
// it creates keep its trace context, which the spans that run them link to.
func TestJobCreationContext(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"), db.WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	h := api.NewHandler(api.Config{
		Store: store, Version: "test", Gateway: &fakeGateway{}, TracerProvider: tp,
		Logger: slog.New(slog.DiscardHandler),
	})

	for _, r := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/smsc", smscBody},
		{http.MethodPost, "/api/v1/cards", cardBody()},
		{http.MethodPost, "/api/v1/jobs", `{"iccids":["8988211000000000017"],"operation":"oplmn_read"}`},
	} {
		req := httptest.NewRequestWithContext(t.Context(), r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		if h.ServeHTTP(rec, req); rec.Code >= 300 {
			t.Fatalf("%s %s = %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}

	var (
		request  sdktrace.ReadOnlySpan
		children []string
	)

	ended := spans.Ended()
	for _, s := range ended {
		if s.Name() == "POST /api/v1/jobs" {
			request = s
		}
	}

	if request == nil {
		t.Fatal("no span for POST /api/v1/jobs")
	}

	for _, s := range ended {
		if s.Parent().SpanID() == request.SpanContext().SpanID() {
			children = append(children, s.Name())
		}
	}

	if want := []string{
		"SELECT smsc", "SELECT cards SELECT key_sets", "SELECT jobs SELECT cards INSERT jobs SELECT cards",
		"SELECT jobs",
	}; !slices.Equal(children, want) {
		t.Errorf("children of the request = %q, want %q", children, want)
	}

	jobs, _, err := store.ListJobs(context.Background(), "", 1, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}

	sc := request.SpanContext()
	if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"; jobs[0].TraceParent != want {
		t.Fatalf("job traceparent %q, want the request's %q", jobs[0].TraceParent, want)
	}
}
