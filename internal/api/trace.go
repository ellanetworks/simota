package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ellanetworks/simota/internal/tracing"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// untraced are the API paths that are polled, by the kubelet's probes, or that serve a static document.
var untraced = map[string]bool{"/api/v1/livez": true, "/api/v1/readyz": true, "/api/v1/openapi.yaml": true}

// traced gives each request to the API a server span, named {method} {route} by the mux's pattern, with its
// http.route. A caller's trace context is linked rather than continued, since the API has no authentication and anyone
// can send one. The UI's files and the untraced paths have no span. A request that fails is an error of its span,
// with error.type its 5xx status; its cause is the error that writeError logs within the span, which carries the
// trace context.
func traced(tp trace.TracerProvider, mux http.Handler) http.Handler {
	if tp == nil {
		return mux
	}

	return otelhttp.NewHandler(routed(mux), "",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(tracing.Propagator),
		otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, "/api/") && !untraced[r.URL.Path]
		}),
	)
}

// routed completes the request's span with what otelhttp leaves to the server, once the mux has handled the request:
// http.route, which otelhttp sets on a span only from a pattern known at its start, and, for a 5xx response,
// error.type, its status, as the HTTP semantic conventions require of a request that ended with an error. otelhttp
// names the span by the pattern and sets its status.
func routed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			// The UI's files and the untraced paths keep the writer as it is, with its io.ReaderFrom.
			next.ServeHTTP(w, r)
			return
		}

		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)

		if i := strings.IndexByte(r.Pattern, '/'); i >= 0 {
			span.SetAttributes(semconv.HTTPRoute(r.Pattern[i:]))
		}

		if sw.status >= http.StatusInternalServerError {
			span.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(sw.status)))
		}
	})
}

// statusWriter records the status of a response.
type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(status int) {
	// An informational status precedes the final one.
	if w.status == 0 && status >= http.StatusOK {
		w.status = status
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	return w.ResponseWriter.Write(b)
}

// Unwrap gives http.ResponseController the writer underneath, for its flushes and deadlines.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
