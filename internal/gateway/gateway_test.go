package gateway_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/gateway"
	"github.com/ellanetworks/smsc/smpp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

const rejected = "15550000002"

func smsc(t *testing.T) (int, func() []string) {
	t.Helper()

	var (
		mu    sync.Mutex
		binds []string
	)

	srv, err := smpp.NewServer(smpp.ServerConfig{
		SystemID: "EllaSMSC",
		Authenticate: func(_ context.Context, req smpp.BindRequest) smpp.BindOutcome {
			mu.Lock()

			binds = append(binds, req.Bind.SystemID)
			mu.Unlock()

			if req.Bind.Password != "secret" || req.Bind.ID != smpp.CmdBindTransceiver {
				return smpp.BindOutcome{Status: smpp.StatusBindFail}
			}

			return smpp.BindOutcome{Status: smpp.StatusOK}
		},
		Handler: smpp.HandlerFunc(func(_ context.Context, _ *smpp.Session, req smpp.Request) smpp.Response {
			if sm, ok := req.PDU.(*smpp.SubmitSM); ok && sm.Destination.Addr == rejected {
				return smpp.Response{Status: smpp.StatusSysErr}
			}

			return smpp.Response{PDU: &smpp.SubmitSMResp{MessageID: "7"}}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = srv.Serve(l) }()

	t.Cleanup(func() { _ = srv.Close() })

	return l.Addr().(*net.TCPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string{}, binds...)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestGateway(t *testing.T) {
	port, binds := smsc(t)
	g := gateway.New(gateway.Config{})

	t.Cleanup(func() { _ = g.Close(context.Background()) })

	bound := func() float64 { return testutil.ToFloat64(g.Collectors()[0]) }
	submits := func(result string) uint64 {
		t.Helper()

		var m dto.Metric
		if err := g.Collectors()[1].(*prometheus.HistogramVec).WithLabelValues(result).(prometheus.Metric).Write(&m); err != nil {
			t.Fatal(err)
		}

		return m.GetHistogram().GetSampleCount()
	}

	if st := g.State(); st.Status != gateway.StatusUnset || bound() != 0 {
		t.Fatalf("unset state %+v, bound %v", st, bound())
	}

	if _, err := g.SubmitSM(context.Background(), &smpp.SubmitSM{}); err == nil {
		t.Fatal("submit without an SMSC")
	}

	s := gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "secret"}
	if err := g.Apply(t.Context(), &s); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "a bind", g.Bound)

	if st := g.State(); st.Status != gateway.StatusBound || st.SystemID != "EllaSMSC" || st.LastError != "" || bound() != 1 {
		t.Fatalf("bound state %+v, bound %v", st, bound())
	}

	if id, err := g.SubmitSM(context.Background(), &smpp.SubmitSM{Message: smpp.Message{
		Destination: smpp.Address{TON: 1, NPI: 1, Addr: "15550000001"},
	}}); err != nil || id != "7" {
		t.Fatalf("SubmitSM = %q, %v", id, err)
	}

	if _, err := g.SubmitSM(context.Background(), &smpp.SubmitSM{Message: smpp.Message{
		Destination: smpp.Address{TON: 1, NPI: 1, Addr: rejected},
	}}); err == nil {
		t.Fatal("SubmitSM refused by the SMSC succeeded")
	}

	if a, r, e := submits("accepted"), submits("rejected"), submits("error"); a != 1 || r != 1 || e != 0 {
		t.Fatalf("submits accepted %d rejected %d error %d, want 1 1 0", a, r, e)
	}

	same := s
	if err := g.Apply(t.Context(), &same); err != nil {
		t.Fatal(err)
	}

	if n := len(binds()); n != 1 {
		t.Fatalf("%d binds after the same settings", n)
	}

	s.Password = "wrong"
	if err := g.Apply(t.Context(), &s); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "a refused bind", func() bool { return g.State().LastError != "" })

	if st := g.State(); st.Status == gateway.StatusBound || st.LastError != "Bind refused: ESME_RBINDFAIL" || bound() != 0 {
		t.Fatalf("refused state %+v, bound %v", st, bound())
	}

	if err := g.Apply(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	if st := g.State(); st.Status != gateway.StatusUnset {
		t.Fatalf("state %+v", st)
	}

	if err := g.Apply(t.Context(), &gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "123456789"}); err == nil {
		t.Fatal("a password of 9 characters was applied")
	}
}
