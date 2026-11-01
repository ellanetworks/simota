package tracing_test

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

// TestTraceParentRoundTrip checks that the traceparent of a recorded span links back to it, and that a span simota
// does not record, such as a caller's trace context while tracing is off, gives none.
func TestTraceParentRoundTrip(t *testing.T) {
	tracer := sdktrace.NewTracerProvider().Tracer("test")

	ctx, span := tracer.Start(context.Background(), "created")
	defer span.End()

	other, command := tracer.Start(context.Background(), "sent")
	defer command.End()

	links := tracing.Links(tracing.TraceParent(ctx), "", "garbage", tracing.TraceParent(other))
	if len(links) != 2 || links[0].SpanContext.TraceID() != span.SpanContext().TraceID() ||
		links[0].SpanContext.SpanID() != span.SpanContext().SpanID() ||
		links[1].SpanContext.SpanID() != command.SpanContext().SpanID() {
		t.Fatalf("Links = %+v, want one to %v then one to %v", links, span.SpanContext(), command.SpanContext())
	}

	remote := trace.ContextWithRemoteSpanContext(context.Background(), span.SpanContext())
	untraced, _ := noop.NewTracerProvider().Tracer("test").Start(remote, "span")

	for name, ctx := range map[string]context.Context{
		"no span":       context.Background(),
		"remote parent": remote,
		"not recorded":  untraced,
	} {
		if got := tracing.TraceParent(ctx); got != "" {
			t.Errorf("TraceParent with %s = %q, want none", name, got)
		}
	}

	for _, invalid := range []string{"", "garbage", "00-00000000000000000000000000000000-b7ad6b7169203331-01"} {
		if links := tracing.Links(invalid); links != nil {
			t.Errorf("Links(%q) = %+v, want none", invalid, links)
		}
	}
}

// TestNewProvider checks that the spans name simota and its version, sampled, and that the OTEL_* environment
// variables do not apply: the configuration file is the only one.
func TestNewProvider(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "simota-west")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=simota-east")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	// No collector listens on port 1, so that the test exports nothing.
	tp, err := tracing.NewProvider(t.Context(), "127.0.0.1:1", "v1.2.3", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	rec := tracetest.NewSpanRecorder()
	tp.RegisterSpanProcessor(rec)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_ = tp.Shutdown(ctx)
	})

	_, span := tp.Tracer("test").Start(t.Context(), "span")
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 || !spans[0].SpanContext().IsSampled() {
		t.Fatalf("recorded %d spans, want 1 sampled", len(spans))
	}

	res := attribute.NewSet(spans[0].Resource().Attributes()...)
	service, _ := res.Value("service.name")
	version, _ := res.Value("service.version")

	if service.AsString() != tracing.ServiceName || version.AsString() != "v1.2.3" {
		t.Fatalf("service %q version %q, want %q v1.2.3", service.AsString(), version.AsString(), tracing.ServiceName)
	}
}

// collector is an OTLP/gRPC trace collector that keeps what it receives.
type collector struct {
	collectortrace.UnimplementedTraceServiceServer

	mu    sync.Mutex
	spans []*tracepb.ResourceSpans
}

func (c *collector) Export(_ context.Context, req *collectortrace.ExportTraceServiceRequest) (
	*collectortrace.ExportTraceServiceResponse, error,
) {
	c.mu.Lock()
	c.spans = append(c.spans, req.GetResourceSpans()...)
	c.mu.Unlock()

	return &collectortrace.ExportTraceServiceResponse{}, nil
}

// TestExport checks that the provider exports its spans to the collector at its endpoint, over plain gRPC, and that
// shutting it down exports the spans left.
func TestExport(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	c := &collector{}
	srv := grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(srv, c)

	go func() { _ = srv.Serve(l) }()

	t.Cleanup(srv.Stop)

	tp, err := tracing.NewProvider(t.Context(), l.Addr().String(), "v1.2.3", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	_, span := tp.Tracer("test").Start(t.Context(), "exported")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tp.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var service string

	names := []string{}

	for _, rs := range c.spans {
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				service = kv.GetValue().GetStringValue()
			}
		}

		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				names = append(names, s.GetName())
			}
		}
	}

	if service != tracing.ServiceName || len(names) != 1 || names[0] != "exported" {
		t.Fatalf("collector got service %q spans %v, want %q [exported]", service, names, tracing.ServiceName)
	}
}
