package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/config"
	"github.com/ellanetworks/simota/internal/logging"
	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

const (
	e2eICCID = "8988211000000000017"
	// callerTraceParent is the trace context that the API's caller sends.
	callerTraceParent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
)

// collector is an OTLP/gRPC trace collector that keeps the spans it receives, and the service.name of their resource.
type collector struct {
	collectortrace.UnimplementedTraceServiceServer

	mu       sync.Mutex
	services map[string]bool
	spans    []*tracepb.Span
}

func (c *collector) Export(_ context.Context, req *collectortrace.ExportTraceServiceRequest) (
	*collectortrace.ExportTraceServiceResponse, error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, rs := range req.GetResourceSpans() {
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				c.services[kv.GetValue().GetStringValue()] = true
			}
		}

		for _, ss := range rs.GetScopeSpans() {
			c.spans = append(c.spans, ss.GetSpans()...)
		}
	}

	return &collectortrace.ExportTraceServiceResponse{}, nil
}

func startCollector(t *testing.T) (*collector, string) {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	c := &collector{services: map[string]bool{}}
	srv := grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(srv, c)

	go func() { _ = srv.Serve(l) }()

	t.Cleanup(srv.Stop)

	return c, l.Addr().String()
}

// startSMSC starts an SMSC that takes simota's bind and submit_sm, and answers each submit_sm with a delivery receipt
// UNDELIV: the card is unreachable.
func startSMSC(t *testing.T) int {
	t.Helper()

	var srv *smpp.Server

	srv, err := smpp.NewServer(smpp.ServerConfig{
		SystemID: "EllaSMSC",
		Authenticate: func(context.Context, smpp.BindRequest) smpp.BindOutcome {
			return smpp.BindOutcome{Status: smpp.StatusOK}
		},
		Handler: smpp.HandlerFunc(func(_ context.Context, _ *smpp.Session, req smpp.Request) smpp.Response {
			sm := req.PDU.(*smpp.SubmitSM)

			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				state := smpp.StateUndeliverable
				receipt := &smpp.DeliverSM{Message: smpp.Message{
					Source: sm.Destination, Destination: sm.Source, ESMClass: 0x04,
					ShortMessage: []byte("id:m1 sub:001 dlvrd:000 submit date:2611011200 done date:2611011201 stat:UNDELIV " +
						"err:000 text:"),
				}}
				receipt.Options.ReceiptedMessageID = new("m1")
				receipt.Options.MessageState = &state

				_, _ = srv.Deliver(ctx, "simota", receipt)
			}()

			return smpp.Response{PDU: &smpp.SubmitSMResp{MessageID: "m1"}}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = srv.Serve(l) }()

	t.Cleanup(func() { _ = srv.Close() })

	return l.Addr().(*net.TCPAddr).Port
}

// freePort is a port free on the loopback interface.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Close() }()

	return l.Addr().(*net.TCPAddr).Port
}

// syncBuffer is a buffer that the logger writes to while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

type client struct {
	t    *testing.T
	base string
}

func (c client) do(method, path, traceparent, body string) (int, string) {
	c.t.Helper()

	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}

	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

func (c client) must(method, path, traceparent, body string, want int) string {
	c.t.Helper()

	code, b := c.do(method, path, traceparent, body)
	if code != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, code, b, want)
	}

	return b
}

// TestTraceEndToEnd runs simota as main does, with telemetry on, and follows a job from the API to the SMSC's receipt:
// the spans exported over OTLP form the trace model of simota's spans, and the logs carry their trace context.
func TestTraceEndToEnd(t *testing.T) {
	coll, endpoint := startCollector(t)
	smscPort := startSMSC(t)
	apiPort := freePort(t)
	logs := &syncBuffer{}

	cfg := config.Config{
		Logging:   config.Logging{Level: slog.LevelDebug},
		DB:        config.DB{Path: filepath.Join(t.TempDir(), "simota.db")},
		API:       config.API{Address: netip.MustParseAddr("127.0.0.1"), Port: apiPort},
		Telemetry: config.Telemetry{Enabled: true, OTLPEndpoint: endpoint},
	}

	ctx, stop := context.WithCancel(context.Background())

	var runErr error

	stopped := make(chan struct{})

	go func() {
		runErr = run(ctx, cfg, logging.New(logs, cfg.Logging.Level))

		close(stopped)
	}()

	t.Cleanup(func() {
		stop()
		<-stopped
	})

	c := client{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", apiPort)}

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if code, _ := c.do(http.MethodGet, "/api/v1/readyz", "", ""); code == http.StatusNoContent {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("simota not ready")
		}
	}

	c.must(http.MethodPut, "/api/v1/smsc", "", fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"system_id":"simota",`+
		`"password":"secret","originator":"+15550001000","validity_seconds":60}`, smscPort), http.StatusOK)
	c.must(http.MethodPost, "/api/v1/cards", "", `{"iccid":"`+e2eICCID+`","imsi":"001010000000001","msisdn":"+15550000001",`+
		`"tar":"b00001","key_sets":[{"version":1,"algorithm":"3des","kic":"`+strings.Repeat("11", 16)+`","kid":"`+
		strings.Repeat("22", 16)+`"}]}`, http.StatusCreated)

	var created struct {
		Result struct{ Items []struct{ ID int64 } }
	}

	if err := json.Unmarshal([]byte(c.must(http.MethodPost, "/api/v1/jobs", callerTraceParent,
		`{"iccids":["`+e2eICCID+`"],"operation":"oplmn_read"}`, http.StatusCreated)), &created); err != nil ||
		len(created.Result.Items) != 1 {
		t.Fatalf("created %+v, %v", created, err)
	}

	id := created.Result.Items[0].ID

	// The receipt UNDELIV closes the read without a PoR: failed.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, b := c.do(http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d", id), "", ""); strings.Contains(b, `"status":"failed"`) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("job %d not closed by the receipt; logs:\n%s", id, logs)
		}
	}

	// Stopping exports the spans left.
	stop()
	<-stopped

	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	checkTrace(t, coll, logs.String(), id)
}

// checkTrace checks the spans of job id as the collector received them, and the logs' trace context.
func checkTrace(t *testing.T, coll *collector, logs string, id int64) {
	t.Helper()

	coll.mu.Lock()
	defer coll.mu.Unlock()

	if !coll.services[tracing.ServiceName] || len(coll.services) != 1 {
		t.Fatalf("services %v, want %s", coll.services, tracing.ServiceName)
	}

	one := func(name string) *tracepb.Span {
		t.Helper()

		var found []*tracepb.Span

		for _, s := range coll.spans {
			if s.GetName() == name {
				found = append(found, s)
			}
		}

		if len(found) != 1 {
			t.Fatalf("%d spans %q, want 1", len(found), name)
		}

		return found[0]
	}

	request, process, submit, deliver := one("POST /api/v1/jobs"), one("process jobs"), one("submit_sm"), one("deliver_sm")

	// The request links to its caller's trace context, and starts a trace of its own.
	if len(request.GetParentSpanId()) != 0 || request.GetKind() != tracepb.Span_SPAN_KIND_SERVER ||
		len(request.GetLinks()) != 1 || hex.EncodeToString(request.GetLinks()[0].GetTraceId()) != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("request span: parent %x kind %v links %v", request.GetParentSpanId(), request.GetKind(), request.GetLinks())
	}

	// The job's processing is a trace of its own, linked to the request, with the submit_sm within it.
	if len(process.GetParentSpanId()) != 0 || process.GetKind() != tracepb.Span_SPAN_KIND_CONSUMER ||
		!links(process, request) || attr(process, "ella.ota.job.outcome") != "sent" ||
		attr(process, "ella.ota.job.id") != fmt.Sprint(id) || attr(process, "ella.ota.card.iccid") != e2eICCID {
		t.Errorf("process span: parent %x kind %v links %v attributes %v", process.GetParentSpanId(), process.GetKind(),
			process.GetLinks(), process.GetAttributes())
	}

	if !bytes.Equal(submit.GetParentSpanId(), process.GetSpanId()) || submit.GetKind() != tracepb.Span_SPAN_KIND_CLIENT ||
		attr(submit, "rpc.response.status_code") != "ESME_ROK" {
		t.Errorf("submit_sm span: parent %x kind %v attributes %v", submit.GetParentSpanId(), submit.GetKind(),
			submit.GetAttributes())
	}

	// The receipt is a trace of its own, linked to the request and to the command it is about.
	if len(deliver.GetParentSpanId()) != 0 || deliver.GetKind() != tracepb.Span_SPAN_KIND_SERVER ||
		!links(deliver, request, process) || attr(deliver, "ella.ota.deliver.kind") != "receipt" ||
		attr(deliver, "ella.ota.deliver.outcome") != "recorded" || attr(deliver, "ella.ota.job.status") != "failed" {
		t.Errorf("deliver_sm span: parent %x kind %v links %v attributes %v", deliver.GetParentSpanId(), deliver.GetKind(),
			deliver.GetLinks(), deliver.GetAttributes())
	}

	// Each has the store's operations within it.
	for _, s := range []*tracepb.Span{request, process, deliver} {
		if children := childrenOf(coll.spans, s); len(children) == 0 {
			t.Errorf("span %q has no store operation within it", s.GetName())
		}
	}

	// No span carries the card's IMSI or MSISDN.
	for _, s := range coll.spans {
		for _, kv := range s.GetAttributes() {
			if v := kv.GetValue().GetStringValue(); strings.Contains(v, "001010000000001") || strings.Contains(v, "15550000001") {
				t.Errorf("span %q: %s = %q", s.GetName(), kv.GetKey(), v)
			}
		}
	}

	// The job's closing is logged in the receipt's trace.
	if !strings.Contains(logs, `"msg":"job finished"`) {
		t.Fatalf("no job finished in the logs:\n%s", logs)
	}

	for line := range strings.Lines(logs) {
		if strings.Contains(line, `"msg":"job finished"`) && (!strings.Contains(line, `"trace_id":"`+hex.EncodeToString(deliver.GetTraceId())+`"`) ||
			!strings.Contains(line, `"span_id":"`+hex.EncodeToString(deliver.GetSpanId())+`"`)) {
			t.Errorf("job finished outside the receipt's span: %s", line)
		}
	}
}

// links reports whether s links, in order, to exactly the spans to.
func links(s *tracepb.Span, to ...*tracepb.Span) bool {
	if len(s.GetLinks()) != len(to) {
		return false
	}

	for i, l := range s.GetLinks() {
		if !bytes.Equal(l.GetTraceId(), to[i].GetTraceId()) || !bytes.Equal(l.GetSpanId(), to[i].GetSpanId()) {
			return false
		}
	}

	return true
}

func childrenOf(spans []*tracepb.Span, parent *tracepb.Span) []*tracepb.Span {
	var out []*tracepb.Span

	for _, s := range spans {
		if bytes.Equal(s.GetParentSpanId(), parent.GetSpanId()) {
			out = append(out, s)
		}
	}

	return out
}

func attr(s *tracepb.Span, key string) string {
	for _, kv := range s.GetAttributes() {
		if kv.GetKey() == key {
			return anyValue(kv.GetValue())
		}
	}

	return ""
}

func anyValue(v *commonpb.AnyValue) string {
	if s, ok := v.GetValue().(*commonpb.AnyValue_IntValue); ok {
		return fmt.Sprint(s.IntValue)
	}

	return v.GetStringValue()
}
