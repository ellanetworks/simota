package jobs_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/ota"
	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const processName = "process jobs"

func (e *env) named(name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range e.spans.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}

	return out
}

func (e *env) children(s sdktrace.ReadOnlySpan) []string {
	var out []string

	for _, c := range e.spans.Ended() {
		if c.Parent().SpanID() == s.SpanContext().SpanID() {
			out = append(out, c.Name())
		}
	}

	return out
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	m := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value.String()
	}

	return m
}

func traceParentOf(s sdktrace.ReadOnlySpan) string {
	return "00-" + s.SpanContext().TraceID().String() + "-" + s.SpanContext().SpanID().String() + "-01"
}

func linksTo(s sdktrace.ReadOnlySpan, traceparents ...string) bool {
	links := s.Links()
	if len(links) != len(traceparents) {
		return false
	}

	for i, l := range links {
		if "00-"+l.SpanContext.TraceID().String()+"-"+l.SpanContext.SpanID().String()+"-01" != traceparents[i] {
			return false
		}
	}

	return true
}

func (e *env) queueTraced(t *testing.T, j db.Job) (int64, string) {
	t.Helper()

	ctx, span := e.tracer.Start(context.Background(), "POST /api/v1/jobs")
	defer span.End()

	j.ICCID = iccid

	ids, err := e.store.CreateJobs(ctx, []db.Job{j})
	if err != nil {
		t.Fatal(err)
	}

	return ids[0], tracing.TraceParent(ctx)
}

func TestIdleStepUntraced(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	before := len(e.spans.Ended())

	e.step()
	e.step()

	if spans := e.spans.Ended()[before:]; len(spans) != 0 {
		t.Fatalf("idle steps ended %d spans, first %q", len(spans), spans[0].Name())
	}
}

func TestProcessSpan(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id, created := e.queueTraced(t, db.Job{Operation: db.OperationOPLMNRead})

	e.step()

	processes := e.named(processName)
	if len(processes) != 1 {
		t.Fatalf("ended %d process spans, want 1", len(processes))
	}

	p := processes[0]
	if p.SpanKind() != trace.SpanKindConsumer || p.Parent().IsValid() || !linksTo(p, created) ||
		p.Status().Code != codes.Unset {
		t.Fatalf("process span kind %v parent %v links %+v status %v", p.SpanKind(), p.Parent(), p.Links(), p.Status())
	}

	a := spanAttrs(p)
	for k, want := range map[attribute.Key]string{
		"messaging.system": "ella_simota", "messaging.destination.name": "jobs", "messaging.operation.name": "process",
		"messaging.operation.type": "process", "messaging.message.id": strconv.FormatInt(id, 10),
		"ella.ota.card.iccid": iccid, "ella.ota.job.operation": "oplmn_read", "ella.ota.job.step": "0",
		"ella.ota.job.outcome": "sent", "ella.ota.job.id": strconv.FormatInt(id, 10),
	} {
		if a[k] != want {
			t.Errorf("%s = %q, want %q", k, a[k], want)
		}
	}

	for _, k := range []attribute.Key{"ella.ota.card.imsi", "ella.ota.card.msisdn"} {
		if _, ok := a[k]; ok {
			t.Errorf("the process span has %s", k)
		}
	}

	if got, want := e.children(p), []string{
		"SELECT cards SELECT key_sets", "UPDATE key_sets", "submit_sm", "UPDATE jobs",
	}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("children of the process span = %q, want %q", got, want)
	}

	submits := e.named("submit_sm")
	if len(submits) != 1 {
		t.Fatalf("ended %d submit_sm spans, want 1", len(submits))
	}

	s, sa := submits[0], spanAttrs(submits[0])
	if s.SpanKind() != trace.SpanKindClient || s.Status().Code != codes.Unset || sa["rpc.system.name"] != "smpp" ||
		sa["rpc.method"] != "submit_sm" || sa["rpc.response.status_code"] != "ESME_ROK" ||
		sa["server.address"] != "127.0.0.1" || sa["server.port"] != strconv.Itoa(e.settings.Port) {
		t.Errorf("submit_sm kind %v status %v attributes %v", s.SpanKind(), s.Status(), sa)
	}

	if j := e.job(t, id); j.TraceParent != created || j.CommandTraceParent != traceParentOf(p) {
		t.Errorf("job traceparent %q command %q, want %q and the process span's %q", j.TraceParent,
			j.CommandTraceParent, created, traceParentOf(p))
	}
}

func TestProcessSpanSteps(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id, created := e.queueTraced(t, db.Job{Operation: db.OperationOPLMNRead})

	e.step()
	e.answer(t, id, ota.StatusPoROK, fcp(60))
	e.step()

	processes := e.named(processName)
	if len(processes) != 2 {
		t.Fatalf("ended %d process spans, want 2", len(processes))
	}

	first, second := processes[0], processes[1]
	if !linksTo(second, created, traceParentOf(first)) || spanAttrs(second)["ella.ota.job.step"] != "1" ||
		second.SpanContext().TraceID() == first.SpanContext().TraceID() {
		t.Fatalf("second command's span: links %+v step %q, want links to the job's creation and the first command",
			second.Links(), spanAttrs(second)["ella.ota.job.step"])
	}
}

func TestProcessSpanOutcomes(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.smsc.setStatus(smpp.StatusThrottled)
		e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		p := e.named(processName)
		s := e.named("submit_sm")

		if len(p) != 1 || spanAttrs(p[0])["ella.ota.job.outcome"] != "retry" || p[0].Status().Code != codes.Unset {
			t.Fatalf("process spans %d, outcome %q status %v", len(p), spanAttrs(p[0])["ella.ota.job.outcome"],
				p[0].Status())
		}

		if sa := spanAttrs(s[0]); len(s) != 1 || s[0].Status().Code != codes.Error ||
			sa["error.type"] != "ESME_RTHROTTLED" || sa["rpc.response.status_code"] != "ESME_RTHROTTLED" {
			t.Fatalf("submit_sm status %v attributes %v", s[0].Status(), sa)
		}
	})

	t.Run("failed", func(t *testing.T) {
		e := newEnv(t, "", sja5(t))
		e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		p := e.named(processName)
		if a := spanAttrs(p[0]); len(p) != 1 || a["ella.ota.job.outcome"] != "failed" || a["error.type"] != "failed" ||
			p[0].Status().Code != codes.Error || p[0].Status().Description != "The card has no MSISDN." {
			t.Fatalf("process span attributes %v status %v", a, p[0].Status())
		}

		if s := e.named("submit_sm"); len(s) != 0 {
			t.Fatalf("a job that failed before sending made %d submit_sm spans", len(s))
		}
	})
}

func TestExpiredSpan(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id, created := e.queueTraced(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()

	sent := e.job(t, id).CommandTraceParent

	e.now = e.now.Add(24*time.Hour + 7*time.Hour)
	e.step()

	processes := e.named(processName)
	if len(processes) != 2 {
		t.Fatalf("ended %d process spans, want 2", len(processes))
	}

	x := processes[1]
	if a := spanAttrs(x); a["ella.ota.job.outcome"] != "expired" || a["error.type"] != "expired" ||
		x.Status().Code != codes.Error || x.Parent().IsValid() || !linksTo(x, created, sent) {
		t.Fatalf("expired span attributes %v status %v links %+v", a, x.Status(), x.Links())
	}

	if got := e.children(x); len(got) != 1 || got[0] != "UPDATE jobs UPDATE cards" {
		t.Fatalf("children of the expired span = %q", got)
	}
}

func (e *env) delivered(t *testing.T, n int) []sdktrace.ReadOnlySpan {
	t.Helper()

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if spans := e.named("deliver_sm"); len(spans) >= n {
			return spans
		}

		if time.Now().After(deadline) {
			t.Fatalf("%d deliver_sm spans ended, want %d", len(e.named("deliver_sm")), n)
		}
	}
}

func TestDeliverSpans(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id, created := e.queueTraced(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()

	sent := e.job(t, id).CommandTraceParent

	e.answer(t, id, ota.StatusPoROK, "039000ffffff0000")

	spans := e.delivered(t, 2)
	por, receipt := spans[0], spans[1]

	a := spanAttrs(por)
	if por.SpanKind() != trace.SpanKindServer || por.Parent().IsValid() || !linksTo(por, created, sent) ||
		por.Status().Code != codes.Unset {
		t.Fatalf("PoR span kind %v parent %v links %+v status %v", por.SpanKind(), por.Parent(), por.Links(), por.Status())
	}

	for k, want := range map[attribute.Key]string{
		"rpc.system.name": "smpp", "rpc.method": "deliver_sm", "rpc.response.status_code": "ESME_ROK",
		"network.peer.address": "127.0.0.1", "ella.ota.deliver.kind": "por", "ella.ota.deliver.outcome": "recorded",
		"ella.ota.job.status": "succeeded", "ella.ota.job.id": strconv.FormatInt(id, 10), "ella.ota.card.iccid": iccid,
	} {
		if a[k] != want {
			t.Errorf("PoR span %s = %q, want %q", k, a[k], want)
		}
	}

	if got := e.children(por); len(got) == 0 || got[len(got)-1] != "UPDATE jobs UPDATE cards" {
		t.Errorf("children of the PoR span = %q, want the job's finishing last", got)
	}

	if a := spanAttrs(receipt); a["ella.ota.deliver.kind"] != "receipt" || a["ella.ota.deliver.outcome"] != "unmatched" ||
		len(receipt.Links()) != 0 || a["ella.ota.job.id"] != "" {
		t.Errorf("receipt span attributes %v links %+v, want an unmatched receipt", a, receipt.Links())
	}
}

func TestDeliverSpanOutcomes(t *testing.T) {
	t.Run("receipt then late PoR", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id, created := e.queueTraced(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()

		j := e.job(t, id)
		e.smsc.deliver(t, e.receipt(j, smpp.StateExpired))
		e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusPoROK, "039000ffffff0000")))

		spans := e.delivered(t, 2)

		if a := spanAttrs(spans[0]); a["ella.ota.deliver.outcome"] != "recorded" || a["ella.ota.job.status"] != "unconfirmed" ||
			!linksTo(spans[0], created, j.CommandTraceParent) {
			t.Errorf("receipt span attributes %v links %+v", a, spans[0].Links())
		}

		if a := spanAttrs(spans[1]); a["ella.ota.deliver.outcome"] != "revised" || a["ella.ota.job.status"] != "succeeded" ||
			!linksTo(spans[1], created, j.CommandTraceParent) {
			t.Errorf("late PoR span attributes %v links %+v", a, spans[1].Links())
		}
	})

	t.Run("card refuses", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()
		e.smsc.deliver(t, e.simAnswer(e.job(t, id), e.por(t, id, ota.StatusTARUnknown, "")))

		s := e.delivered(t, 1)[0]
		if a := spanAttrs(s); a["ella.ota.job.status"] != "failed" || a["ella.ota.deliver.outcome"] != "recorded" ||
			s.Status().Code != codes.Unset || a["error.type"] != "" {
			t.Errorf("span attributes %v status %v, want a failed job and no error of the deliver_sm", a, s.Status())
		}
	})

	t.Run("unmatched PoR", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()
		e.smsc.deliver(t, e.simAnswer(e.job(t, id), e.porOf(t, tar, 7, ota.StatusPoROK, "")))

		s := e.delivered(t, 1)[0]
		if a := spanAttrs(s); a["ella.ota.deliver.kind"] != "por" || a["ella.ota.deliver.outcome"] != "unmatched" ||
			len(s.Links()) != 0 {
			t.Errorf("span attributes %v links %+v", a, s.Links())
		}
	})

	t.Run("neither PoR nor receipt", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.smsc.deliver(t, &smpp.DeliverSM{Message: smpp.Message{
			Source: smpp.Address{TON: 1, NPI: 1, Addr: "15550000001"}, ShortMessage: []byte("hello"),
		}})

		s := e.delivered(t, 1)[0]
		if a := spanAttrs(s); a["ella.ota.deliver.kind"] != "other" || a["ella.ota.deliver.outcome"] != "ignored" {
			t.Errorf("span attributes %v", a)
		}
	})

	t.Run("not recorded", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()
		e.faults.set(func(s *faultyStore) { s.finish = errDB })

		if err := e.smsc.try(e.simAnswer(e.job(t, id), e.por(t, id, ota.StatusPoROK, "039000ffffff0000"))); err == nil {
			t.Fatal("deliver_sm_resp ESME_ROK, want ESME_RX_T_APPN")
		}

		s := e.delivered(t, 1)[0]
		if a := spanAttrs(s); s.Status().Code != codes.Error || a["error.type"] != "ESME_RX_T_APPN" ||
			a["rpc.response.status_code"] != "ESME_RX_T_APPN" || a["ella.ota.deliver.outcome"] != "" ||
			!strings.Contains(s.Status().Description, "database is locked") {
			t.Errorf("span attributes %v status %+v", a, s.Status())
		}
	})
}
