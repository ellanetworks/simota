package smsc_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/smsc"
)

func server(t *testing.T, h http.HandlerFunc) *smsc.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"Unauthorized"}`)

			return
		}

		h(w, r)
	}))
	t.Cleanup(srv.Close)

	return &smsc.Client{BaseURL: srv.URL + "/", Token: "token"}
}

func TestSubmitBinary(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/messages" || r.Header.Get("Content-Type") != "application/json" ||
			string(b) != `{"from":"+15550001000","to":"+15550000001","binary":{"protocol_identifier":127,"data_coding_scheme":246,"user_data_header":"027000","user_data":"cafe"}}` {
			t.Errorf("%s %s %s", r.Method, r.URL.Path, b)
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"result":{"items":[{"id":42,"status":"pending"}]}}`)
	})

	id, err := c.SubmitBinary(context.Background(), "+15550001000", "+15550000001", smsc.Binary{
		ProtocolIdentifier: smsc.ProtocolUSIMDataDownload, DataCodingScheme: smsc.DCSClass2,
		UserDataHeader: []byte{0x02, 0x70, 0x00}, UserData: []byte{0xca, 0xfe},
	})
	if err != nil || id != 42 {
		t.Fatalf("SubmitBinary = %d, %v", id, err)
	}
}

func TestSubmitBinaryNeedsOneMessage(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"items":[{"id":1},{"id":2}]}}`)
	})

	if _, err := c.SubmitBinary(context.Background(), "a", "b", smsc.Binary{}); err == nil {
		t.Fatal("expected an error for two messages queued")
	}
}

func TestErrors(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"Invalid binary message"}`)
	})

	_, err := c.Message(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "Invalid binary message") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}

	c.Token = "wrong"
	if _, err := c.Message(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401", err)
	}

	plain := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	if _, err := plain.Message(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want 502", err)
	}
}

func TestWait(t *testing.T) {
	var polls atomic.Int32

	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		if polls.Add(1) < 3 {
			_, _ = io.WriteString(w, `{"result":{"id":7,"status":"pending"}}`)
			return
		}

		_, _ = io.WriteString(w, `{"result":{"id":7,"status":"failed"}}`)
	})

	m, err := c.Wait(context.Background(), 7, time.Millisecond)
	if err != nil || m.Status != smsc.StatusFailed || polls.Load() != 3 {
		t.Fatalf("Wait = %+v, %v after %d polls", m, err, polls.Load())
	}
}

func TestWaitTimesOut(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"id":7,"status":"pending"}}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := c.Wait(ctx, 7, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestDeliverReports(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/messages/7/content" {
			t.Errorf("path %s", r.URL.Path)
		}

		_, _ = io.WriteString(w, `{"result":{"binary":{},"deliver_reports":[{"attempt_id":3,"user_data":"0271"},{"attempt_id":4}]}}`)
	})

	reports, err := c.DeliverReports(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}

	if len(reports) != 2 || reports[0].AttemptID != 3 || string(reports[0].UserData) != "\x02\x71" || reports[1].UserData != nil {
		t.Fatalf("reports = %+v", reports)
	}
}
