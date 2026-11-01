// Package tracing exports simota's traces over OTLP, and carries a trace context across the jobs that simota stores
// and runs later.
package tracing

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the service.name of simota's spans, as Ella SMSC's are ella-smsc.
const ServiceName = "ella-simota"

// MessagingSystem is the messaging.system of the spans that process a stored job, as Ella SMSC's processing of a
// stored message is ella_smsc.
const MessagingSystem = "ella_simota"

// Propagator is the only trace context simota reads and writes: W3C Trace Context, without baggage.
var Propagator = propagation.TraceContext{}

// NewProvider returns a provider that exports to an OTLP/gRPC collector at endpoint, a host:port, without TLS. It
// samples every trace that a caller has not decided against. The configuration file is its only configuration: it
// reads no OTEL_* environment variable that would change the service, the sampling or the endpoint. It connects
// lazily, so that an unreachable collector does not hold simota's start; it reports export failures to logger.
func NewProvider(ctx context.Context, endpoint, version string, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(ServiceName), semconv.ServiceVersion(version)),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.WarnContext(context.Background(), "OpenTelemetry error", slog.Any("error", err))
	}))

	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	), nil
}

// TraceParent returns the W3C traceparent of the span in ctx, to store with a job that ctx creates or a command that
// ctx sends, or "" when that span is not recorded: when tracing is off, ctx can still carry a caller's trace context,
// which no span of simota would link to.
func TraceParent(ctx context.Context) string {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ""
	}

	carrier := propagation.MapCarrier{}
	Propagator.Inject(ctx, carrier)

	return carrier.Get("traceparent")
}

// Links returns a link to the span of each stored traceparent, for a span that processes what was stored with them,
// skipping those that are empty or invalid.
func Links(traceparents ...string) []trace.Link {
	var links []trace.Link

	for _, tp := range traceparents {
		if tp == "" {
			continue
		}

		sc := trace.SpanContextFromContext(Propagator.Extract(context.Background(), propagation.MapCarrier{"traceparent": tp}))
		if sc.IsValid() {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}

	return links
}
