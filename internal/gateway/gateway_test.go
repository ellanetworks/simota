package gateway_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/gateway"
	"github.com/ellanetworks/smsc/smpp"
)

// smsc is an SMSC on a loopback port that accepts the password "secret", and records the system_id of each bind.
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
		Handler: smpp.HandlerFunc(func(context.Context, *smpp.Session, smpp.Request) smpp.Response {
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

	if st := g.State(); st.Status != gateway.StatusUnset {
		t.Fatalf("unset state %+v", st)
	}

	if _, err := g.SubmitSM(context.Background(), &smpp.SubmitSM{}); err == nil {
		t.Fatal("submit without an SMSC")
	}

	s := gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "secret"}
	if err := g.Apply(&s); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "a bind", g.Bound)

	if st := g.State(); st.Status != gateway.StatusBound || st.SystemID != "EllaSMSC" || st.LastError != "" {
		t.Fatalf("bound state %+v", st)
	}

	if id, err := g.SubmitSM(context.Background(), &smpp.SubmitSM{Message: smpp.Message{
		Destination: smpp.Address{TON: 1, NPI: 1, Addr: "15550000001"},
	}}); err != nil || id != "7" {
		t.Fatalf("SubmitSM = %q, %v", id, err)
	}

	// The same settings again keep the session.
	same := s
	if err := g.Apply(&same); err != nil {
		t.Fatal(err)
	}

	if n := len(binds()); n != 1 {
		t.Fatalf("%d binds after the same settings", n)
	}

	// A wrong password: the session is replaced, and the refusal shows.
	s.Password = "wrong"
	if err := g.Apply(&s); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "a refused bind", func() bool { return g.State().LastError != "" })

	if st := g.State(); st.Status == gateway.StatusBound || !strings.Contains(st.LastError, "ESME_RBINDFAIL") {
		t.Fatalf("refused state %+v", st)
	}

	// Unset.
	if err := g.Apply(nil); err != nil {
		t.Fatal(err)
	}

	if st := g.State(); st.Status != gateway.StatusUnset {
		t.Fatalf("state %+v", st)
	}

	// Settings that cannot bind are refused at once.
	if err := g.Apply(&gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "123456789"}); err == nil {
		t.Fatal("a password of 9 characters was applied")
	}
}
