package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/db"
)

type env struct {
	srv *httptest.Server
}

func newEnv(t *testing.T) env {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	srv := httptest.NewServer(api.NewHandler(api.Config{Store: store, Version: "test", Logger: slog.New(slog.DiscardHandler)}))
	t.Cleanup(srv.Close)

	return env{srv: srv}
}

func (e env) do(t *testing.T, method, path, contentType, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

var (
	kic = strings.Repeat("11", 16)
	kid = strings.Repeat("22", 16)
)

const newCard = `{"iccid":"8988211000000000017","imsi":"001010000000001","msisdn":"+15550000001","tar":"b00010",
	"key_sets":[{"version":1,"algorithm":"3des","kic":"` + "%KIC%" + `","kid":"` + "%KID%" + `"}]}`

func cardBody() string {
	return strings.NewReplacer("%KIC%", kic, "%KID%", kid).Replace(newCard)
}

func TestCardLifecycle(t *testing.T) {
	e := newEnv(t)

	code, body := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", cardBody())
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}

	if strings.Contains(body, kic) || strings.Contains(body, kid) {
		t.Fatalf("response holds key material: %s", body)
	}

	var created struct{ Result api.Card }
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	if created.Result.KVN != 1 || created.Result.TAR != "b00010" || len(created.Result.KeySets) != 1 ||
		created.Result.KeySets[0].Algorithm != "3des" {
		t.Fatalf("card = %+v", created.Result)
	}

	if code, _ := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", cardBody()); code != http.StatusConflict {
		t.Fatalf("duplicate create = %d", code)
	}

	code, body = e.do(t, http.MethodPut, "/api/v1/cards/8988211000000000017", "application/json",
		`{"imsi":"001010000000001","msisdn":"+15550000002","description":"A56","tar":"b00010","kvn":1}`)
	if code != http.StatusOK || !strings.Contains(body, `"msisdn":"+15550000002"`) {
		t.Fatalf("update = %d %s", code, body)
	}

	if code, _ := e.do(t, http.MethodPut, "/api/v1/cards/8988211000000000017", "application/json",
		`{"imsi":"001010000000001","tar":"b00010","kvn":2}`); code != http.StatusBadRequest {
		t.Fatalf("update to a missing key set = %d", code)
	}

	if code, body := e.do(t, http.MethodGet, "/api/v1/cards", "", ""); code != http.StatusOK || !strings.Contains(body, `"description":"A56"`) {
		t.Fatalf("list = %d %s", code, body)
	}

	if code, _ := e.do(t, http.MethodDelete, "/api/v1/cards/8988211000000000017", "", ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}

	if code, _ := e.do(t, http.MethodGet, "/api/v1/cards/8988211000000000017", "", ""); code != http.StatusNotFound {
		t.Fatalf("get deleted = %d", code)
	}
}

func TestCreateCardRejects(t *testing.T) {
	e := newEnv(t)

	for _, tc := range []struct{ from, to string }{
		{`"iccid":"8988211000000000017"`, `"iccid":"12"`},
		{`"imsi":"001010000000001"`, `"imsi":"x"`},
		{`"msisdn":"+15550000001"`, `"msisdn":"15550000001"`},
		{`"tar":"b00010"`, `"tar":"b000"`},
		{`"algorithm":"3des"`, `"algorithm":"des"`},
		{kic, "0011"},
	} {
		body := strings.Replace(cardBody(), tc.from, tc.to, 1)
		if code, _ := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", body); code != http.StatusBadRequest {
			t.Fatalf("create with %s = %d, want 400", tc.to, code)
		}
	}
}

func TestImportCards(t *testing.T) {
	e := newEnv(t)

	if code, body := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", cardBody()); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}

	file := "ICCID\tIMSI\tKi\tKIC1\tKID1\tKIC2\tKID2\n" +
		"8988211000000000017\t999700000000001\t" + strings.Repeat("ff", 16) + "\t" + kic + "\t" + kid + "\t" + kic + "\t" + kid + "\n" +
		"8988211000000000025\t999700000000002\t" + strings.Repeat("ff", 16) + "\t" + kic + "\t" + kid + "\t\t\n"

	code, body := e.do(t, http.MethodPost, "/api/v1/cards/import", "text/plain", file)
	if code != http.StatusOK || !strings.Contains(body, `"created":1`) || !strings.Contains(body, `"updated":1`) {
		t.Fatalf("import = %d %s", code, body)
	}

	_, body = e.do(t, http.MethodGet, "/api/v1/cards/8988211000000000017", "", "")

	var c struct{ Result api.Card }
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}

	// The import replaces the IMSI and key sets, and keeps the MSISDN.
	if c.Result.IMSI != "999700000000001" || c.Result.MSISDN != "+15550000001" || len(c.Result.KeySets) != 2 {
		t.Fatalf("card = %+v", c.Result)
	}

	if code, _ := e.do(t, http.MethodPost, "/api/v1/cards/import", "text/plain", "ICCID\n1\n"); code != http.StatusBadRequest {
		t.Fatalf("bad import = %d", code)
	}
}

func TestFrontend(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<html>index</html>")},
		"assets/app.js": {Data: []byte("app")},
		"favicon.svg":   {Data: []byte("<svg/>")},
	}
	srv := httptest.NewServer(api.NewHandler(api.Config{Frontend: ui, Logger: slog.New(slog.DiscardHandler)}))
	defer srv.Close()

	for _, tc := range []struct {
		path, body string
		code       int
		cache      bool
	}{
		{"/", "<html>index</html>", http.StatusOK, false},
		{"/cards", "<html>index</html>", http.StatusOK, false},
		{"/assets/app.js", "app", http.StatusOK, true},
		{"/favicon.svg", "<svg/>", http.StatusOK, false},
		{"/api/v1/unknown", "", http.StatusNotFound, false},
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}

		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != tc.code || (tc.body != "" && string(b) != tc.body) ||
			(resp.Header.Get("Cache-Control") != "") != tc.cache {
			t.Fatalf("%s = %d %q, cache %q", tc.path, resp.StatusCode, b, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestBadRequests(t *testing.T) {
	e := newEnv(t)

	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/api/v1/cards", "{", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/cards", `{"iccid":"8988211000000000017","imsi":"001010000000001","key_sets":[]}`, http.StatusBadRequest},
		{http.MethodPut, "/api/v1/cards/8988211000000000017", `{"imsi":"001010000000001"}`, http.StatusNotFound},
		{http.MethodPut, "/api/v1/cards/8988211000000000017", "{", http.StatusBadRequest},
		{http.MethodDelete, "/api/v1/cards/8988211000000000017", "", http.StatusNotFound},
		{http.MethodGet, "/api/v1/status", "", http.StatusOK},
	} {
		if code, body := e.do(t, tc.method, tc.path, "application/json", tc.body); code != tc.code {
			t.Fatalf("%s %s %s = %d %s, want %d", tc.method, tc.path, tc.body, code, body, tc.code)
		}
	}
}
