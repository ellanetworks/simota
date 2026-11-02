package jobs

import (
	"context"
	"net"
	"strconv"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const tracerName = "github.com/ellanetworks/simota/internal/jobs"

const (
	processOperation = "process"
	jobsDestination  = "jobs"
)

const (
	jobIDKey     = attribute.Key("ella.ota.job.id")
	iccidKey     = attribute.Key("ella.ota.card.iccid")
	operationKey = attribute.Key("ella.ota.job.operation")
	stepKey      = attribute.Key("ella.ota.job.step")
	outcomeKey   = attribute.Key("ella.ota.job.outcome")
	statusKey    = attribute.Key("ella.ota.job.status")

	deliverKindKey    = attribute.Key("ella.ota.deliver.kind")
	deliverOutcomeKey = attribute.Key("ella.ota.deliver.outcome")
)

const (
	outcomeSent    = "sent"
	outcomeRetry   = "retry"
	outcomeWaiting = "waiting"
	outcomeFailed  = "failed"
	outcomeExpired = "expired"
)

func (r *Runner) tracer() trace.Tracer {
	tp := r.TracerProvider
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))
}

func (r *Runner) startProcess(ctx context.Context, j db.Job) (context.Context, trace.Span) {
	return r.tracer().Start(ctx, processOperation+" "+jobsDestination,
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithLinks(tracing.Links(j.TraceParent, j.CommandTraceParent)...),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String(tracing.MessagingSystem),
			semconv.MessagingDestinationName(jobsDestination),
			semconv.MessagingOperationName(processOperation),
			semconv.MessagingOperationTypeProcess,
			semconv.MessagingMessageID(strconv.FormatInt(j.ID, 10)),
		),
		trace.WithAttributes(jobAttributes(j)...),
	)
}

func jobAttributes(j db.Job) []attribute.KeyValue {
	return []attribute.KeyValue{
		jobIDKey.Int64(j.ID),
		iccidKey.String(j.ICCID),
		operationKey.String(string(j.Operation)),
		stepKey.Int(j.Step),
	}
}

func setOutcome(ctx context.Context, outcome, reason string) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(outcomeKey.String(outcome))

	if outcome == outcomeFailed || outcome == outcomeExpired {
		span.SetAttributes(semconv.ErrorTypeKey.String(outcome))
		span.SetStatus(codes.Error, reason)
	}
}

const (
	deliverPoR     = "por"
	deliverReceipt = "receipt"
	deliverOther   = "other"
)

const (
	deliverRecorded  = "recorded"
	deliverRevised   = "revised"
	deliverIgnored   = "ignored"
	deliverUnmatched = "unmatched"
)

func (r *Runner) startDeliver(ctx context.Context, s *smpp.Session) (context.Context, trace.Span) {
	attrs := tracing.SMPPAttributes(smpp.CmdDeliverSM)

	if s != nil {
		if addr, ok := s.RemoteAddr().(*net.TCPAddr); ok {
			attrs = append(attrs, semconv.NetworkPeerAddress(addr.IP.String()), semconv.NetworkPeerPort(addr.Port),
				semconv.NetworkTransportTCP)
		}
	}

	return r.tracer().Start(ctx, smpp.CmdDeliverSM.String(), trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attrs...))
}

func deliverKind(ctx context.Context, kind string) {
	trace.SpanFromContext(ctx).SetAttributes(deliverKindKey.String(kind))
}

func deliverMatched(ctx context.Context, j db.Job) {
	span := trace.SpanFromContext(ctx)

	for _, l := range tracing.Links(j.TraceParent, j.CommandTraceParent) {
		span.AddLink(l)
	}

	span.SetAttributes(jobAttributes(j)...)
}

func deliverOutcome(ctx context.Context, outcome string, status db.JobStatus) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(deliverOutcomeKey.String(outcome))

	if status != "" {
		span.SetAttributes(statusKey.String(string(status)))
	}
}
