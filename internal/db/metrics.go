package db

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const countTimeout = 2 * time.Second

var (
	operations    = []Operation{OperationOPLMNRead, OperationOPLMNSet, OperationOPLMNClear, OperationIMSIWrite}
	finalStatuses = []JobStatus{JobSucceeded, JobUnconfirmed, JobFailed}
	openStatuses  = []JobStatus{JobQueued, JobSent}
)

func newDuration() *prometheus.HistogramVec {
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ellasimota_job_duration_seconds",
		Help: "Time from creation to success of the jobs that succeeded, by operation (oplmn_read, oplmn_set, " +
			"oplmn_clear, imsi_write), the time the card was unreachable included. Unconfirmed and failed jobs are left out.",
		Buckets:                         []float64{1, 2.5, 5, 10, 30, 60, 120, 300, 900, 3600, 21600, 86400, 604800},
		NativeHistogramBucketFactor:     1.1,
		NativeHistogramMaxBucketNumber:  100,
		NativeHistogramMinResetDuration: time.Hour,
	}, []string{"operation"})

	for _, op := range operations {
		duration.WithLabelValues(string(op))
	}

	return duration
}

func newCompleted() *prometheus.CounterVec {
	completed := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ellasimota_jobs_completed_total",
		Help: "Jobs that reached a final status, by operation (oplmn_read, oplmn_set, oplmn_clear, imsi_write) and " +
			"status (succeeded, unconfirmed, failed). A late PoR that revises a job's final status does not count it again.",
	}, []string{"operation", "status"})

	for _, op := range operations {
		for _, status := range finalStatuses {
			completed.WithLabelValues(string(op), string(status))
		}
	}

	return completed
}

var jobsDesc = prometheus.NewDesc(
	"ellasimota_jobs_pending",
	"Jobs not yet final, by status: queued (waiting to be sent, or between two steps) or sent (waiting for the "+
		"card's PoR). Queued jobs that stay queued mean simota cannot send them, such as when the SMSC is down.",
	[]string{"status"}, nil,
)

type jobsCollector struct {
	d *DB
}

func (c jobsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- jobsDesc
}

func (c jobsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), countTimeout)
	defer cancel()

	counts, err := c.d.countOpen(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(jobsDesc, err)
		return
	}

	for _, status := range openStatuses {
		ch <- prometheus.MustNewConstMetric(jobsDesc, prometheus.GaugeValue, float64(counts[status]), string(status))
	}
}

func (d *DB) countOpen(ctx context.Context) (map[JobStatus]int, error) {
	rows, err := d.conn.QueryContext(ctx, "SELECT status, count(*) FROM jobs WHERE status IN ('queued', 'sent') GROUP BY status")
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	counts := map[JobStatus]int{}

	for rows.Next() {
		var (
			status JobStatus
			n      int
		)

		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}

		counts[status] = n
	}

	return counts, rows.Err()
}

func (d *DB) Collectors() []prometheus.Collector {
	return []prometheus.Collector{d.completed, d.duration, jobsCollector{d}}
}
