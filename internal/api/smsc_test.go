package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/gateway"
)

// fakeGateway records the settings applied, and shows the state the test sets.
type fakeGateway struct {
	mu      sync.Mutex
	applied []gateway.Settings
	state   gateway.State
}

func (f *fakeGateway) Apply(s *gateway.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if s == nil {
		return errors.New("unexpected disconnect")
	}

	f.applied = append(f.applied, *s)

	return nil
}

func (f *fakeGateway) State() gateway.State {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.state.Status == "" {
		return gateway.State{Status: gateway.StatusUnset}
	}

	return f.state
}

const smscBody = `{"host":"127.0.0.1","port":2775,"tls":false,"system_id":"simota","password":"secret",` +
	`"originator":"+15550001000","validity_seconds":86400}`

// smscResult decodes GET or PUT /api/v1/smsc.
func smscResult(t *testing.T, body string) map[string]any {
	t.Helper()

	var r struct{ Result map[string]any }
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	return r.Result
}

func TestSMSC(t *testing.T) {
	e := newEnv(t)

	// Unset: the defaults, no password, not bound, and no jobs.
	code, body := e.do(t, http.MethodGet, "/api/v1/smsc", "", "")

	got := smscResult(t, body)
	if code != http.StatusOK || got["host"] != "" || got["port"] != 2775.0 || got["password_set"] != false ||
		got["validity_seconds"] != 86400.0 || got["bind"].(map[string]any)["status"] != "unset" ||
		got["bind"].(map[string]any)["bound"] != nil {
		t.Fatalf("unset = %d %s", code, body)
	}

	if code, b := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", cardBody()); code != http.StatusCreated {
		t.Fatalf("create card = %d %s", code, b)
	}

	if code, body := e.do(t, http.MethodPost, "/api/v1/jobs", "application/json",
		`{"iccids":["8988211000000000017"],"operation":"oplmn_read"}`); code != http.StatusConflict ||
		!strings.Contains(body, "Set the SMSC") {
		t.Fatalf("job without an SMSC = %d %s", code, body)
	}

	if code, _ := e.do(t, http.MethodPut, "/api/v1/smsc", "application/json",
		strings.Replace(smscBody, `"password":"secret",`, "", 1)); code != http.StatusBadRequest {
		t.Fatalf("first SMSC without a password = %d", code)
	}

	code, body = e.do(t, http.MethodPut, "/api/v1/smsc", "application/json", smscBody)
	if code != http.StatusOK || strings.Contains(body, "secret") || !strings.Contains(body, `"password_set":true`) {
		t.Fatalf("put = %d %s", code, body)
	}

	if *e.notified != 1 || len(e.gateway.applied) != 1 || e.gateway.applied[0] != (gateway.Settings{
		Host: "127.0.0.1", Port: 2775, SystemID: "simota", Password: "secret",
	}) {
		t.Fatalf("runner notified %d times, gateway applied %+v", *e.notified, e.gateway.applied)
	}

	// An update without a password keeps it, and the port defaults to 2775; the password is never returned.
	code, body = e.do(t, http.MethodPut, "/api/v1/smsc", "application/json",
		`{"host":"smsc.example","tls":true,"system_id":"ota","originator":"+15550001000","validity_seconds":3600}`)

	got = smscResult(t, body)
	if code != http.StatusOK || got["host"] != "smsc.example" || got["port"] != 2775.0 || got["tls"] != true ||
		got["system_id"] != "ota" || got["validity_seconds"] != 3600.0 || got["password_set"] != true ||
		strings.Contains(body, "secret") || strings.Contains(body, "password\"") {
		t.Fatalf("update = %d %s", code, body)
	}

	if a := e.gateway.applied[1]; a.Password != "secret" || !a.TLS || a.Host != "smsc.example" {
		t.Fatalf("applied %+v", a)
	}

	// GET shows the bind state.
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	e.gateway.state = gateway.State{
		Status: gateway.StatusDisconnected, Since: at, LastError: "smpp: bind refused: smpp: bind_transceiver_resp ESME_RBINDFAIL",
		LastErrorAt: at,
	}

	_, body = e.do(t, http.MethodGet, "/api/v1/smsc", "", "")
	if b := smscResult(t, body)["bind"].(map[string]any); b["bound"] != nil || b["status"] != "disconnected" ||
		!strings.Contains(b["last_error"].(string), "ESME_RBINDFAIL") || b["last_error_at"] != "2026-10-28T12:00:00Z" {
		t.Fatalf("disconnected = %s", body)
	}

	e.gateway.state = gateway.State{Status: gateway.StatusBound, Since: at, SystemID: "EllaSMSC"}

	_, body = e.do(t, http.MethodGet, "/api/v1/smsc", "", "")
	if b := smscResult(t, body)["bind"].(map[string]any); b["bound"] != nil || b["status"] != "bound" ||
		b["smsc_system_id"] != "EllaSMSC" || b["since"] != "2026-10-28T12:00:00Z" || b["last_error"] != nil {
		t.Fatalf("bound = %s", body)
	}

	if code, _ := e.do(t, http.MethodPost, "/api/v1/jobs", "application/json",
		`{"iccids":["8988211000000000017"],"operation":"oplmn_read"}`); code != http.StatusCreated {
		t.Fatalf("job with an SMSC = %d", code)
	}
}

func TestSMSCRejects(t *testing.T) {
	e := newEnv(t)

	for _, body := range []string{
		strings.Replace(smscBody, `"127.0.0.1"`, `""`, 1),
		strings.Replace(smscBody, `"127.0.0.1"`, `"http://127.0.0.1"`, 1),
		strings.Replace(smscBody, `"127.0.0.1"`, `"127.0.0.1:2775"`, 1),
		strings.Replace(smscBody, `"127.0.0.1"`, `"fe80::1%eth0"`, 1),
		strings.Replace(smscBody, "2775", "65536", 1),
		strings.Replace(smscBody, "2775", "-1", 1),
		strings.Replace(smscBody, `"tls":false`, `"tls":"no"`, 1),
		strings.Replace(smscBody, `"simota"`, `""`, 1),
		strings.Replace(smscBody, `"simota"`, `"0123456789abcdef"`, 1),
		strings.Replace(smscBody, `"secret"`, `"123456789"`, 1),
		strings.Replace(smscBody, `"secret"`, `"sécret"`, 1),
		strings.Replace(smscBody, "+15550001000", "15550001000", 1),
		strings.Replace(smscBody, "86400", "0", 1),
		// L3: at most 99 days, so that the relative validity_period stays normalised.
		strings.Replace(smscBody, "86400", "8553601", 1),
		strings.Replace(smscBody, `"validity_seconds"`, `"url":"http://x","validity_seconds"`, 1),
	} {
		if code, b := e.do(t, http.MethodPut, "/api/v1/smsc", "application/json", body); code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", body, code, b)
		}
	}

	if code, b := e.do(t, http.MethodPut, "/api/v1/smsc", "application/json",
		strings.Replace(smscBody, "86400", "8553600", 1)); code != http.StatusOK {
		t.Fatalf("validity of 99 days = %d %s", code, b)
	}

	// An IPv6 address and a host name are hosts.
	for _, host := range []string{"2001:db8::1", "smsc-1.example.com"} {
		if code, b := e.do(t, http.MethodPut, "/api/v1/smsc", "application/json",
			strings.Replace(smscBody, `"127.0.0.1"`, `"`+host+`"`, 1)); code != http.StatusOK {
			t.Fatalf("host %s = %d %s", host, code, b)
		}
	}
}
