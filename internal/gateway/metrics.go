package gateway

import (
	"errors"
	"time"

	"github.com/ellanetworks/smsc/smpp"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	submitAccepted = "accepted"
	submitRejected = "rejected"
	submitError    = "error"
)

func newSubmitDuration() *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ellasimota_smsc_submit_duration_seconds",
		Help: "Time from a submit_sm sent to the SMSC to its submit_sm_resp, by result: accepted, rejected (the SMSC " +
			"answered with an error status) or error (no answer, such as a timeout or a lost connection). A submit " +
			"without a bound session is not sent, and is left out.",
		Buckets:                         []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		NativeHistogramBucketFactor:     1.1,
		NativeHistogramMaxBucketNumber:  100,
		NativeHistogramMinResetDuration: time.Hour,
	}, []string{"result"})

	for _, r := range []string{submitAccepted, submitRejected, submitError} {
		h.WithLabelValues(r)
	}

	return h
}

func (g *Gateway) observeSubmit(start time.Time, err error) {
	var se *smpp.StatusError

	result := submitAccepted

	switch {
	case errors.Is(err, smpp.ErrNoSession):
		return
	case errors.As(err, &se):
		result = submitRejected
	case err != nil:
		result = submitError
	}

	g.submit.WithLabelValues(result).Observe(time.Since(start).Seconds())
}

func (g *Gateway) Collectors() []prometheus.Collector {
	return []prometheus.Collector{prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "ellasimota_smsc_bound",
		Help: "1 when simota is bound to the SMSC, 0 otherwise, including when no SMSC is configured.",
	}, func() float64 {
		if g.Bound() {
			return 1
		}

		return 0
	}), g.submit}
}
