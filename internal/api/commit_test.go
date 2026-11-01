package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
)

// disconnectingStore is the store of a client that goes away just after its change is committed: it cancels the
// request's context once PutSMSC or CreateJobs succeeds, as net/http does when the connection closes.
type disconnectingStore struct {
	*db.DB

	cancel context.CancelFunc
}

func (s *disconnectingStore) PutSMSC(ctx context.Context, smsc db.SMSC) (db.SMSC, error) {
	stored, err := s.DB.PutSMSC(ctx, smsc)
	if err == nil {
		s.cancel()
	}

	return stored, err
}

func (s *disconnectingStore) CreateJobs(ctx context.Context, jobs []db.Job) ([]int64, error) {
	ids, err := s.DB.CreateJobs(ctx, jobs)
	if err == nil {
		s.cancel()
	}

	return ids, err
}

// TestCommittedChangeOutlivesClient checks that the effects of a committed change, applying the SMSC settings and
// waking the runner, happen though the client goes away before the response.
func TestCommittedChangeOutlivesClient(t *testing.T) {
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	notified := 0
	gw := &fakeGateway{}
	s := &disconnectingStore{DB: store}
	h := api.NewHandler(api.Config{
		Store: s, Version: "test", Notify: func() { notified++ }, Gateway: gw, Logger: slog.New(slog.DiscardHandler),
	})

	do := func(method, path, body string) {
		t.Helper()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		s.cancel = cancel

		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	do(http.MethodPut, "/api/v1/smsc", smscBody)

	want := gateway.Settings{Host: "127.0.0.1", Port: 2775, SystemID: "simota", Password: "secret"}
	if len(gw.applied) != 1 || gw.applied[0] != want || notified != 1 {
		t.Fatalf("after PUT /smsc: applied %+v, notified %d", gw.applied, notified)
	}

	if err := store.CreateCard(context.Background(), db.Card{
		ICCID: "8988211000000000017", MSISDN: "+15550000001", TAR: []byte{0xb0, 0x00, 0x01}, KVN: 1,
		KeySets: []db.KeySet{{Version: 1, Algorithm: "3des", KIc: make([]byte, 16), KID: make([]byte, 16)}},
	}); err != nil {
		t.Fatal(err)
	}

	do(http.MethodPost, "/api/v1/jobs", `{"iccids":["8988211000000000017"],"operation":"oplmn_read"}`)

	if notified != 2 {
		t.Fatalf("after POST /jobs: notified %d", notified)
	}
}
