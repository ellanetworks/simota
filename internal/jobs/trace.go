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

// A job's spans are those of a consumer of a queue, the jobs, as Ella SMSC's delivery attempts of a stored message
// are (OpenTelemetry semantic conventions, messaging spans): named {messaging.operation.name} {destination}.
const (
	processOperation = "process"
	jobsDestination  = "jobs"
)

// The attributes of a job's spans that the conventions do not name. Every span of a job carries its ID, as
// ella.ota.job.id, and its card's ICCID, but no IMSI, MSISDN, key material or job error, which may tell an IMSI.
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

// The outcomes of the processing of a job, its ella.ota.job.outcome.
const (
	// outcomeSent is a command that the SMSC took.
	outcomeSent = "sent"
	// outcomeRetry is a command that the SMSC did not take, which a later step sends again.
	outcomeRetry = "retry"
	// outcomeWaiting is a job left queued, untried, while the session is unbound or the store fails.
	outcomeWaiting = "waiting"
	// outcomeFailed is a job that failed before the SMSC took its command, which sending it again would not change.
	outcomeFailed = "failed"
	// outcomeExpired is a job closed without its card's answer, nor the SMSC's receipt, within the validity.
	outcomeExpired = "expired"
)

func (r *Runner) tracer() trace.Tracer {
	tp := r.TracerProvider
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))
}

// startProcess starts the span of the processing of a job by the runner: the root of a trace of its own, since a job
// runs later than the request that created it, linked to that request and to the sending of the job's last command,
// if any: the step before, for a read of several.
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

// jobAttributes are the attributes of the spans of job j.
func jobAttributes(j db.Job) []attribute.KeyValue {
	return []attribute.KeyValue{
		jobIDKey.Int64(j.ID),
		iccidKey.String(j.ICCID),
		operationKey.String(string(j.Operation)),
		stepKey.Int(j.Step),
	}
}

// setOutcome sets the outcome of the processing of a job on the span of ctx: an error, of error.type the outcome, for
// a job that failed or expired, with reason its error. A job that waits, or whose command is sent again later, is not
// one: its retry is a later span's.
func setOutcome(ctx context.Context, outcome, reason string) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(outcomeKey.String(outcome))

	if outcome == outcomeFailed || outcome == outcomeExpired {
		span.SetAttributes(semconv.ErrorTypeKey.String(outcome))
		span.SetStatus(codes.Error, reason)
	}
}

// The kinds of a deliver_sm, its ella.ota.deliver.kind.
const (
	// deliverPoR is the card's answer to a command, its PoR.
	deliverPoR = "por"
	// deliverReceipt is the SMSC's delivery receipt of a command.
	deliverReceipt = "receipt"
	// deliverOther is neither.
	deliverOther = "other"
)

// The outcomes of a deliver_sm, its ella.ota.deliver.outcome.
const (
	// deliverRecorded is a deliver_sm recorded on the job in progress that it is about.
	deliverRecorded = "recorded"
	// deliverRevised is a late PoR that replaces the outcome of a job closed without it.
	deliverRevised = "revised"
	// deliverIgnored is a deliver_sm about no command whose outcome it would tell.
	deliverIgnored = "ignored"
	// deliverUnmatched is a deliver_sm that answers no job's command.
	deliverUnmatched = "unmatched"
)

// startDeliver starts the span of a deliver_sm from the SMSC on session s: a server span of the RPC conventions, the
// root of a trace of its own, since the SMSC sends no trace context, as SMPP 3.4 carries none.
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

// deliverKind sets the kind of the deliver_sm of the span of ctx.
func deliverKind(ctx context.Context, kind string) {
	trace.SpanFromContext(ctx).SetAttributes(deliverKindKey.String(kind))
}

// deliverMatched tells the span of ctx, a deliver_sm's, the job it is about: it links to the request that created the
// job and to the sending of the command that the deliver_sm answers, and carries the job's attributes.
func deliverMatched(ctx context.Context, j db.Job) {
	span := trace.SpanFromContext(ctx)

	for _, l := range tracing.Links(j.TraceParent, j.CommandTraceParent) {
		span.AddLink(l)
	}

	span.SetAttributes(jobAttributes(j)...)
}

// deliverOutcome sets the outcome of a deliver_sm on the span of ctx, and the status of the job it leaves, if any. A
// job that it fails is no error of the deliver_sm, which is recorded: its status tells.
func deliverOutcome(ctx context.Context, outcome string, status db.JobStatus) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(deliverOutcomeKey.String(outcome))

	if status != "" {
		span.SetAttributes(statusKey.String(string(status)))
	}
}
