package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func value(t *testing.T, d *db.DB, name string, labels map[string]string) float64 {
	t.Helper()

	r := prometheus.NewRegistry()
	r.MustRegister(d.Collectors()...)

	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range families {
		if f.GetName() != name {
			continue
		}

		for _, m := range f.GetMetric() {
			if matches(m, labels) {
				switch f.GetType() {
				case dto.MetricType_COUNTER:
					return m.GetCounter().GetValue()
				case dto.MetricType_HISTOGRAM:
					return float64(m.GetHistogram().GetSampleCount())
				default:
					return m.GetGauge().GetValue()
				}
			}
		}
	}

	t.Fatalf("no %s%v", name, labels)

	return 0
}

func matches(m *dto.Metric, labels map[string]string) bool {
	if len(m.GetLabel()) != len(labels) {
		return false
	}

	for _, l := range m.GetLabel() {
		if labels[l.GetName()] != l.GetValue() {
			return false
		}
	}

	return true
}

func TestJobMetrics(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	c := card("8988211000000000017", 1)
	c.MSISDN = "+15550000017"

	if err := d.CreateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	other := card("8988211000000000025", 1)
	other.MSISDN = "+15550000025"

	if err := d.CreateCard(ctx, other); err != nil {
		t.Fatal(err)
	}

	ids, err := d.CreateJobs(ctx, []db.Job{
		{ICCID: c.ICCID, Operation: db.OperationOPLMNRead},
		{ICCID: other.ICCID, Operation: db.OperationOPLMNClear, Slot: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	open := func(queued, sent float64) {
		t.Helper()

		if q, s := value(t, d, "ellasimota_jobs_pending", map[string]string{"status": "queued"}),
			value(t, d, "ellasimota_jobs_pending", map[string]string{"status": "sent"}); q != queued || s != sent {
			t.Fatalf("jobs queued %v sent %v, want %v %v", q, s, queued, sent)
		}
	}

	completed := func(op db.Operation, status db.JobStatus) float64 {
		t.Helper()

		return value(t, d, "ellasimota_jobs_completed_total", map[string]string{"operation": string(op), "status": string(status)})
	}

	open(2, 0)

	if completed(db.OperationIMSIWrite, db.JobFailed) != 0 {
		t.Fatal("completed series do not start at 0")
	}

	if err := d.MarkJobSent(ctx, ids[0], 7, "55", []byte{1}); err != nil {
		t.Fatal(err)
	}

	open(1, 1)

	j, _ := d.GetJob(ctx, ids[0])
	j.Status, j.PoRMissing = db.JobUnconfirmed, true

	if err := d.FinishJob(ctx, j); err != nil {
		t.Fatal(err)
	}

	if err := d.FinishJob(ctx, j); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("finish twice = %v", err)
	}

	j.Status, j.PoRMissing = db.JobSucceeded, false
	if err := d.ReviseJob(ctx, j); err != nil {
		t.Fatal(err)
	}

	if err := d.FinishJob(ctx, db.Job{ID: ids[1], Status: db.JobFailed}); err != nil {
		t.Fatal(err)
	}

	open(0, 0)

	succeeded := func(op db.Operation) float64 {
		t.Helper()

		return value(t, d, "ellasimota_job_duration_seconds", map[string]string{"operation": string(op)})
	}

	if n := succeeded(db.OperationOPLMNRead); n != 0 {
		t.Fatalf("durations of a revised job = %v, want 0", n)
	}

	ids, err = d.CreateJobs(ctx, []db.Job{{ICCID: c.ICCID, Operation: db.OperationOPLMNRead}})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.FinishJob(ctx, db.Job{ID: ids[0], Status: db.JobSucceeded}); err != nil {
		t.Fatal(err)
	}

	if n, other := succeeded(db.OperationOPLMNRead), succeeded(db.OperationOPLMNClear); n != 1 || other != 0 {
		t.Fatalf("durations oplmn_read %v oplmn_clear %v, want 1 0", n, other)
	}

	for _, want := range []struct {
		op     db.Operation
		status db.JobStatus
		n      float64
	}{
		{db.OperationOPLMNRead, db.JobUnconfirmed, 1},
		{db.OperationOPLMNRead, db.JobSucceeded, 1},
		{db.OperationOPLMNClear, db.JobFailed, 1},
	} {
		if n := completed(want.op, want.status); n != want.n {
			t.Errorf("completed %s %s = %v, want %v", want.op, want.status, n, want.n)
		}
	}
}
