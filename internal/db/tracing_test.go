package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// traced is a store traced to a span recorder, and a tracer of the same provider for the callers' spans.
type traced struct {
	*db.DB

	spans  *tracetest.SpanRecorder
	tracer trace.Tracer
}

func openTraced(t *testing.T) traced {
	t.Helper()

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"), db.WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return traced{DB: d, spans: spans, tracer: tp.Tracer("test")}
}

// caller starts the span of a caller of the store.
func (s traced) caller(t *testing.T) (context.Context, trace.Span) {
	t.Helper()

	return s.tracer.Start(t.Context(), "caller")
}

// operations are the ended spans of the store, not the callers'.
func (s traced) operations() []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, span := range s.spans.Ended() {
		if span.Name() != "caller" {
			out = append(out, span)
		}
	}

	return out
}

func attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	m := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value.String()
	}

	return m
}

// TestNoSpanWithoutCaller checks that the store makes no span of its own, as for the runner's poll for jobs.
func TestNoSpanWithoutCaller(t *testing.T) {
	s := openTraced(t)

	if _, err := s.JobsIn(context.Background(), db.JobQueued, 10); err != nil {
		t.Fatal(err)
	}

	if spans := s.spans.Ended(); len(spans) != 0 {
		t.Fatalf("ended %d spans without a caller, first %q", len(spans), spans[0].Name())
	}
}

// TestOperationSpans checks that each operation within a caller's span is a client span, its child, named by its query
// summary, with the operation and the table only when it has one of each.
func TestOperationSpans(t *testing.T) {
	s := openTraced(t)
	ctx, parent := s.caller(t)

	if err := s.CreateCard(ctx, card("8988211000000000017", 1)); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetCard(ctx, "8988211000000000017"); err != nil {
		t.Fatal(err)
	}

	c, err := s.GetCard(ctx, "8988211000000000017")
	if err != nil {
		t.Fatal(err)
	}

	c.Description = "updated"
	if err := s.UpdateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	parent.End()

	want := []struct {
		name                   string
		operation, table       string
		hasOperation, hasTable bool
	}{
		{"INSERT cards INSERT key_sets DELETE key_sets", "", "", false, false},
		{"SELECT cards SELECT key_sets", "SELECT", "", true, false},
		{"SELECT cards SELECT key_sets", "SELECT", "", true, false},
		{"UPDATE cards", "UPDATE", "cards", true, true},
	}

	spans := s.operations()
	if len(spans) != len(want) {
		t.Fatalf("ended %d operation spans, want %d", len(spans), len(want))
	}

	for i, w := range want {
		sp, a := spans[i], attrs(spans[i])

		if sp.Name() != w.name || sp.SpanKind() != trace.SpanKindClient ||
			sp.Parent().SpanID() != parent.SpanContext().SpanID() || sp.Status().Code != codes.Unset {
			t.Errorf("span %d: %q kind %v parent %v status %v", i, sp.Name(), sp.SpanKind(), sp.Parent(), sp.Status())
		}

		if a["db.system.name"] != "sqlite" || a["db.query.summary"] != w.name {
			t.Errorf("span %q: attributes %v", sp.Name(), a)
		}

		if op, ok := a["db.operation.name"]; ok != w.hasOperation || op != w.operation {
			t.Errorf("span %q: db.operation.name %q, want %q", sp.Name(), op, w.operation)
		}

		if table, ok := a["db.collection.name"]; ok != w.hasTable || table != w.table {
			t.Errorf("span %q: db.collection.name %q, want %q", sp.Name(), table, w.table)
		}

		if _, ok := a["db.query.text"]; ok {
			t.Errorf("span %q has the query text", sp.Name())
		}
	}
}

// TestOperationErrors checks that a failure of the database is an error of its span, with the SQLite result code,
// and that an outcome the store reports by design, such as not found, is not.
func TestOperationErrors(t *testing.T) {
	s := openTraced(t)
	ctx, parent := s.caller(t)

	if _, err := s.GetJob(ctx, 42); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetJob = %v, want ErrNotFound", err)
	}

	// The schema refuses a system_id of 16 characters: SQLITE_CONSTRAINT_CHECK.
	if _, err := s.PutSMSC(ctx, db.SMSC{
		Host: "h", Port: 2775, SystemID: "0123456789abcdef", Password: "pw", Originator: "+15550001000", Validity: time.Hour,
	}); err == nil {
		t.Fatal("PutSMSC accepted a system_id of 16 characters")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := s.GetSMSC(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetSMSC = %v, want context.Canceled", err)
	}

	parent.End()

	spans := s.operations()
	if len(spans) != 3 {
		t.Fatalf("ended %d operation spans, want 3", len(spans))
	}

	if sp := spans[0]; sp.Status().Code != codes.Unset || attrs(sp)["error.type"] != "" {
		t.Errorf("not found: status %v, attributes %v", sp.Status(), attrs(sp))
	}

	if sp, a := spans[1], attrs(spans[1]); sp.Status().Code != codes.Error || a["error.type"] != "275" ||
		a["db.response.status_code"] != "275" {
		t.Errorf("constraint: status %v, attributes %v", sp.Status(), a)
	}

	if sp, a := spans[2], attrs(spans[2]); sp.Status().Code != codes.Error || a["error.type"] != "canceled" {
		t.Errorf("canceled: status %v, attributes %v", sp.Status(), a)
	}
}

// TestStoredTraceContext checks that a job keeps the trace context of the caller that created it, and of the one that
// sent its command, not that of the store's own span, and none when untraced.
func TestStoredTraceContext(t *testing.T) {
	s := openTraced(t)

	if err := s.CreateCard(context.Background(), card("8988211000000000017", 1)); err != nil {
		t.Fatal(err)
	}

	untraced, err := s.CreateJobs(context.Background(), []db.Job{{ICCID: "8988211000000000017", Operation: db.OperationOPLMNRead}})
	if err != nil {
		t.Fatal(err)
	}

	if j, err := s.GetJob(context.Background(), untraced[0]); err != nil || j.TraceParent != "" || j.CommandTraceParent != "" {
		t.Fatalf("untraced job = %+v, %v", j, err)
	}

	j := untraced[0]
	if err := s.FinishJob(context.Background(), db.Job{
		ID: j, Status: db.JobFailed, FileSize: -1, PoRStatus: -1,
		StatusWord: -1,
	}); err != nil {
		t.Fatal(err)
	}

	created, creator := s.caller(t)
	wantCreated := tracing.TraceParent(created)

	ids, err := s.CreateJobs(created, []db.Job{{ICCID: "8988211000000000017", Operation: db.OperationOPLMNRead}})
	if err != nil {
		t.Fatal(err)
	}

	creator.End()

	sent, sender := s.caller(t)
	wantSent := tracing.TraceParent(sent)

	if err := s.MarkJobSent(sent, ids[0], 1, "m1", []byte{1}); err != nil {
		t.Fatal(err)
	}

	sender.End()

	got, err := s.GetJob(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}

	if got.TraceParent != wantCreated || got.CommandTraceParent != wantSent || wantCreated == "" || wantSent == "" {
		t.Fatalf("job traceparent %q command %q, want %q and %q", got.TraceParent, got.CommandTraceParent, wantCreated,
			wantSent)
	}
}

// TestCallerSpanLeftOpen checks that a store that makes no span, untraced, leaves the caller's span as it is: open,
// and not an error for an outcome of its own.
func TestCallerSpanLeftOpen(t *testing.T) {
	d, _ := open(t)
	spans := tracetest.NewSpanRecorder()

	ctx, span := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test").Start(t.Context(), "caller")
	defer span.End()

	if _, err := d.GetJob(ctx, 42); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetJob = %v, want ErrNotFound", err)
	}

	if _, err := d.GetSMSC(canceledContext(ctx)); err == nil {
		t.Fatal("GetSMSC with a canceled context succeeded")
	}

	if ended := spans.Ended(); len(ended) != 0 || !span.IsRecording() {
		t.Fatalf("the store ended %d spans, the caller's recording %v", len(ended), span.IsRecording())
	}
}

func canceledContext(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	cancel()

	return ctx
}
