package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/db"
	"gopkg.in/yaml.v3"
)

type env struct {
	srv      *httptest.Server
	store    *db.DB
	notified *int
	gateway  *fakeGateway
}

func newEnv(t *testing.T) env {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	notified := new(int)
	gw := &fakeGateway{}
	srv := httptest.NewServer(api.NewHandler(api.Config{
		Store: store, Version: "test", Notify: func() { *notified++ }, Gateway: gw, Logger: slog.New(slog.DiscardHandler),
	}))
	t.Cleanup(srv.Close)

	return env{srv: srv, store: store, notified: notified, gateway: gw}
}

func (e env) do(t *testing.T, method, path, contentType, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, e.srv.URL+path, strings.NewReader(body))
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

const newCard = `{"iccid":"8988211000000000017","imsi":"001010000000001","msisdn":"+15550000001","tar":"b00001",
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

	if created.Result.KVN != 1 || created.Result.TAR != "b00001" || len(created.Result.KeySets) != 1 ||
		created.Result.KeySets[0].Algorithm != "3des" {
		t.Fatalf("card = %+v", created.Result)
	}

	if code, _ := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", cardBody()); code != http.StatusConflict {
		t.Fatalf("duplicate create = %d", code)
	}

	code, body = e.do(t, http.MethodPut, "/api/v1/cards/8988211000000000017", "application/json",
		`{"imsi":"001010000000001","msisdn":"+15550000002","description":"A56","tar":"b00001","kvn":1}`)
	if code != http.StatusOK || !strings.Contains(body, `"msisdn":"+15550000002"`) {
		t.Fatalf("update = %d %s", code, body)
	}

	if code, _ := e.do(t, http.MethodPut, "/api/v1/cards/8988211000000000017", "application/json",
		`{"imsi":"001010000000001","tar":"b00001","kvn":2}`); code != http.StatusBadRequest {
		t.Fatalf("update to a missing key set = %d", code)
	}

	// The IMSI is optional: without one, the card shows none.
	code, body = e.do(t, http.MethodPut, "/api/v1/cards/8988211000000000017", "application/json",
		`{"msisdn":"+15550000002","description":"A56","tar":"b00001","kvn":1}`)
	if code != http.StatusOK || strings.Contains(body, `"imsi"`) {
		t.Fatalf("update without an IMSI = %d %s", code, body)
	}

	if code, body := e.do(t, http.MethodGet, "/api/v1/cards", "", ""); code != http.StatusOK || !strings.Contains(body, `"description":"A56"`) {
		t.Fatalf("list = %d %s", code, body)
	}

	if code, body := e.do(t, http.MethodDelete, "/api/v1/cards/8988211000000000017", "", ""); code != http.StatusOK ||
		body != `{"result":{"message":"Card deleted successfully"}}` {
		t.Fatalf("delete = %d %s", code, body)
	}

	if code, _ := e.do(t, http.MethodGet, "/api/v1/cards/8988211000000000017", "", ""); code != http.StatusNotFound {
		t.Fatalf("get deleted = %d", code)
	}
}

func TestCreateCardRejects(t *testing.T) {
	e := newEnv(t)

	for _, tc := range []struct{ from, to string }{
		{`"iccid":"8988211000000000017"`, `"iccid":"12"`},
		{`"imsi":"001010000000001"`, `"imsi":"00101"`},
		{`"imsi":"001010000000001"`, `"imsi":"x"`},
		{`"msisdn":"+15550000001"`, `"msisdn":"15550000001"`},
		{`"tar":"b00001"`, `"tar":"b000"`},
		{`"tar":"b00001"`, `"tar":"b0000f"`},
		{`"tar":"b00001"`, `"tar":"b00120"`},
		{`"tar":"b00001"`, `"tar":"b00000"`},
		{`"tar":"b00001"`, `"tar":"b10011"`},
		{`"algorithm":"3des"`, `"algorithm":"3des","cc_length":4`},
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

	code, body := e.do(t, http.MethodPost, "/api/v1/cards/import?algorithm=3des", "text/plain", file)
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

	for _, path := range []string{
		"/api/v1/cards/import?algorithm=3des", "/api/v1/cards/import", "/api/v1/cards/import?algorithm=des",
		"/api/v1/cards/import?algorithm=3des&cc_length=4", "/api/v1/cards/import?algorithm=aes&cc_length=6",
	} {
		body := file
		if path == "/api/v1/cards/import?algorithm=3des" {
			body = "ICCID\n1\n"
		}

		if code, _ := e.do(t, http.MethodPost, path, "text/plain", body); code != http.StatusBadRequest {
			t.Fatalf("import %s = %d, want 400", path, code)
		}
	}

	// AES-128 keys with 4-octet checksums.
	aes := strings.Replace(file, "8988211000000000025", "8988211000000000033", 1)
	if code, body := e.do(t, http.MethodPost, "/api/v1/cards/import?algorithm=aes&cc_length=4", "text/plain", aes); code != http.StatusOK {
		t.Fatalf("AES import = %d %s", code, body)
	}

	if _, body := e.do(t, http.MethodGet, "/api/v1/cards/8988211000000000033", "", ""); !strings.Contains(body,
		`"key_sets":[{"version":1,"algorithm":"aes","cc_length":4,"counter":0}]`) {
		t.Fatalf("AES card = %s", body)
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
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := http.DefaultClient.Do(req)
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

func TestListCardsPages(t *testing.T) {
	e := newEnv(t)

	for _, iccid := range []string{"8988211000000000017", "8988211000000000025", "8988211000000000033"} {
		body := strings.Replace(cardBody(), "8988211000000000017", iccid, 1)
		if code, b := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", body); code != http.StatusCreated {
			t.Fatalf("create = %d %s", code, b)
		}
	}

	code, body := e.do(t, http.MethodGet, "/api/v1/cards?page=2&per_page=2", "", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}

	var page struct{ Result api.ListCardsResponse }
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}

	if page.Result.Page != 2 || page.Result.PerPage != 2 || page.Result.TotalCount != 3 || len(page.Result.Items) != 1 ||
		page.Result.Items[0].ICCID != "8988211000000000033" {
		t.Fatalf("page = %+v", page.Result)
	}

	// Timestamps have milliseconds, as in Ella SMSC and Ella IMS.
	if ts := page.Result.Items[0].CreatedAt; len(ts) != len("2026-10-28T17:34:53.000Z") || !strings.HasSuffix(ts, "Z") {
		t.Fatalf("created_at = %q", ts)
	}

	_, body = e.do(t, http.MethodGet, "/api/v1/cards", "", "")
	if !strings.Contains(body, `"page":1,"per_page":25,"total_count":3`) {
		t.Fatalf("default page = %s", body)
	}

	for _, q := range []string{"page=0", "page=x", "per_page=0", "per_page=101"} {
		if code, _ := e.do(t, http.MethodGet, "/api/v1/cards?"+q, "", ""); code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", q, code)
		}
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	e := newEnv(t)

	body := strings.Replace(cardBody(), `"tar":"b00001"`, `"tar":"b00001","adm1":"12345678"`, 1)
	if code, b := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", body); code != http.StatusBadRequest ||
		b != `{"error":"Invalid request data"}` {
		t.Fatalf("create with an unknown field = %d %s", code, b)
	}
}

func TestHealthAndSpec(t *testing.T) {
	e := newEnv(t)

	for _, p := range []string{"/api/v1/livez", "/api/v1/readyz"} {
		if code, _ := e.do(t, http.MethodGet, p, "", ""); code != http.StatusNoContent {
			t.Fatalf("%s = %d", p, code)
		}
	}

	if code, body := e.do(t, http.MethodGet, "/api/v1/openapi.yaml", "", ""); code != http.StatusOK || !strings.HasPrefix(body, "openapi: ") {
		t.Fatalf("openapi = %d", code)
	}
}

// TestOpenAPISpecCoversRoutes checks that the specification documents every route.
func TestOpenAPISpecCoversRoutes(t *testing.T) {
	e := newEnv(t)

	_, body := e.do(t, http.MethodGet, "/api/v1/openapi.yaml", "", "")

	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}

	if err := yaml.Unmarshal([]byte(body), &spec); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}

	for _, route := range []string{
		"GET /api/v1/cards", "POST /api/v1/cards", "POST /api/v1/cards/import", "GET /api/v1/cards/{iccid}",
		"PUT /api/v1/cards/{iccid}", "DELETE /api/v1/cards/{iccid}", "GET /api/v1/status", "GET /api/v1/livez",
		"GET /api/v1/readyz", "GET /api/v1/openapi.yaml", "POST /api/v1/jobs", "GET /api/v1/jobs", "GET /api/v1/jobs/{id}",
		"GET /api/v1/smsc", "PUT /api/v1/smsc", "PUT /api/v1/cards/{iccid}/key-sets/{version}",
	} {
		method, path, _ := strings.Cut(route, " ")
		if _, ok := spec.Paths[path][strings.ToLower(method)]; !ok {
			t.Errorf("spec is missing %s", route)
		}
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// TestICCIDPadding checks that an ICCID with the 'F' padding of an odd number of digits names the same card (TS 102 221
// clause 13.2).
func TestICCIDPadding(t *testing.T) {
	e := newEnv(t)

	body := strings.Replace(cardBody(), `"iccid":"8988211000000000017"`, `"iccid":"898821100000000001F"`, 1)
	if code, b := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", body); code != http.StatusCreated ||
		!strings.Contains(b, `"iccid":"898821100000000001"`) {
		t.Fatalf("create = %d %s", code, b)
	}

	if code, _ := e.do(t, http.MethodGet, "/api/v1/cards/898821100000000001f", "", ""); code != http.StatusOK {
		t.Fatalf("get padded = %d", code)
	}
}

func TestUpdateKeySetCounter(t *testing.T) {
	e := newEnv(t)

	if code, b := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", cardBody()); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, b)
	}

	code, body := e.do(t, http.MethodPut, "/api/v1/cards/8988211000000000017/key-sets/1", "application/json", `{"counter":16}`)
	if code != http.StatusOK || !strings.Contains(body, `"counter":16`) {
		t.Fatalf("set counter = %d %s", code, body)
	}

	// The next command takes one more.
	if got, err := e.store.NextCounter(context.Background(), "8988211000000000017", 1); err != nil || got != 17 {
		t.Fatalf("next counter = %d, %v", got, err)
	}

	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/api/v1/cards/8988211000000000017/key-sets/2", `{"counter":1}`, http.StatusNotFound},
		{"/api/v1/cards/8988211000000000025/key-sets/1", `{"counter":1}`, http.StatusNotFound},
		{"/api/v1/cards/8988211000000000017/key-sets/16", `{"counter":1}`, http.StatusBadRequest},
		{"/api/v1/cards/8988211000000000017/key-sets/1", `{}`, http.StatusBadRequest},
		{"/api/v1/cards/8988211000000000017/key-sets/1", `{"counter":1099511627776}`, http.StatusBadRequest},
		{"/api/v1/cards/8988211000000000017/key-sets/1", `{"counter":-1}`, http.StatusBadRequest},
	} {
		if code, b := e.do(t, http.MethodPut, tc.path, "application/json", tc.body); code != tc.code {
			t.Fatalf("PUT %s %s = %d %s, want %d", tc.path, tc.body, code, b, tc.code)
		}
	}
}
