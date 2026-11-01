package db

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/mattn/go-sqlite3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const tracerName = "github.com/ellanetworks/simota/internal/db"

// Option configures a DB.
type Option func(*DB)

// WithTracerProvider traces the operations of the store with tp, when not nil.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(d *DB) {
		if tp != nil {
			d.tracer = tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))
		}
	}
}

// operation is a logical operation of the store, which an exported method makes: a span of the database client
// conventions, named by its query summary (OpenTelemetry semantic conventions, database spans).
type operation struct {
	summary string
	attrs   []attribute.KeyValue
}

// newOperation is the operation of query summary summary: the operations and their targets, in the order the method
// runs its statements, "{operation1} {target1} {operation2} {target2} ...". Its db.operation.name is the one
// operation it may have, and its db.collection.name the one table, as the conventions want them only for a call with a
// single one.
func newOperation(summary string) operation {
	words := strings.Fields(summary)
	if len(words) == 0 || len(words)%2 != 0 {
		panic("db: query summary " + strconv.Quote(summary) + " is not operations and their targets")
	}

	attrs := []attribute.KeyValue{semconv.DBSystemNameSQLite, semconv.DBQuerySummary(summary)}

	ops, tables := map[string]bool{}, map[string]bool{}
	for i := 0; i < len(words); i += 2 {
		ops[words[i]], tables[words[i+1]] = true, true
	}

	if len(ops) == 1 {
		attrs = append(attrs, semconv.DBOperationName(words[0]))
	}

	if len(tables) == 1 {
		attrs = append(attrs, semconv.DBCollectionName(words[1]))
	}

	return operation{summary: summary, attrs: attrs}
}

// The operations of the exported methods.
var (
	opCreateCard   = newOperation("INSERT cards INSERT key_sets DELETE key_sets")
	opNextCounter  = newOperation("UPDATE key_sets")
	opUpdateCard   = newOperation("UPDATE cards")
	opDeleteCard   = newOperation("DELETE cards")
	opGetCard      = newOperation("SELECT cards SELECT key_sets")
	opListCards    = newOperation("SELECT cards SELECT key_sets")
	opImportCards  = newOperation("UPDATE cards INSERT cards INSERT key_sets DELETE key_sets UPDATE cards")
	opSetCounter   = newOperation("UPDATE key_sets")
	opCreateJobs   = newOperation("SELECT jobs SELECT cards INSERT jobs SELECT cards")
	opGetJob       = newOperation("SELECT jobs")
	opListJobs     = newOperation("SELECT jobs SELECT cards")
	opJobsIn       = newOperation("SELECT jobs")
	opSentJob      = newOperation("SELECT jobs")
	opAwaitingPoR  = newOperation("SELECT jobs SELECT cards")
	opMarkJobSent  = newOperation("UPDATE jobs")
	opAdvanceJob   = newOperation("UPDATE jobs")
	opCountAttempt = newOperation("UPDATE jobs")
	opFinishJob    = newOperation("UPDATE jobs UPDATE cards")
	opGetSMSC      = newOperation("SELECT smsc")
	opPutSMSC      = newOperation("SELECT smsc INSERT smsc")
)

// start starts the client span of an operation, only within a span of the caller: the store is called for a request,
// a job or a deliver_sm, whose span it is part of, and also by the runner's poll for jobs, every few seconds, which
// is no trace of its own.
func (d *DB) start(ctx context.Context, op operation) (context.Context, trace.Span) {
	if d.tracer == nil || !trace.SpanContextFromContext(ctx).IsValid() {
		// A span of no operation, which end does not mistake for the caller's.
		return ctx, noop.Span{}
	}

	return d.tracer.Start(ctx, op.summary, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(op.attrs...))
}

// end ends the span of an operation, which failed with err, if any. The outcomes that the store reports by design, such
// as a job not found or a card busy, are the caller's to judge, not failures of the database.
func end(span trace.Span, err error) {
	defer span.End()

	if err == nil || expected(err) {
		return
	}

	var se sqlite3.Error

	switch {
	case errors.As(err, &se):
		code := strconv.Itoa(int(se.ExtendedCode))
		span.SetAttributes(semconv.DBResponseStatusCode(code), semconv.ErrorTypeKey.String(code))
	case errors.Is(err, context.DeadlineExceeded):
		span.SetAttributes(semconv.ErrorTypeKey.String("timeout"))
	case errors.Is(err, context.Canceled):
		span.SetAttributes(semconv.ErrorTypeKey.String("canceled"))
	default:
		span.SetAttributes(semconv.ErrorTypeOther)
	}

	span.SetStatus(codes.Error, err.Error())
}

// expected reports whether err is an outcome that the store reports by design.
func expected(err error) bool {
	for _, e := range []error{ErrNotFound, ErrAlreadyExists, ErrBusy, ErrIMSIMismatch, ErrCounterExhausted} {
		if errors.Is(err, e) {
			return true
		}
	}

	return false
}

// traceParent is the traceparent to store with what ctx creates, read before the span of the operation starts, which
// would otherwise be stored instead of the caller's: NULL when ctx is not traced.
func traceParent(ctx context.Context) any {
	return nullString(tracing.TraceParent(ctx))
}
