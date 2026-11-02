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

var untraced = map[string]bool{"/api/v1/livez": true, "/api/v1/readyz": true, "/api/v1/openapi.yaml": true}

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

func routed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
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

type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(status int) {
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

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
