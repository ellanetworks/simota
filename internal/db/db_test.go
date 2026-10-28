package db_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/db"
)

func open(t *testing.T) (*db.DB, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "simota.db")

	d, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d, path
}

func card(iccid string, versions ...int) db.Card {
	c := db.Card{ICCID: iccid, IMSI: "001010000000001", TAR: []byte{0xb0, 0x00, 0x10}, KVN: versions[0]}
	for _, v := range versions {
		c.KeySets = append(c.KeySets, db.KeySet{
			Version: v, Algorithm: "3des", KIc: bytes.Repeat([]byte{byte(v)}, 16),
			KID: bytes.Repeat([]byte{byte(v + 0x10)}, 16),
		})
	}

	return c
}

func TestOpenRestrictsAndReopens(t *testing.T) {
	d, path := open(t)

	if err := d.CreateCard(context.Background(), card("8988211000000000017", 1)); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}

	_ = d.Close()

	// A second open finds the schema current and keeps the data.
	d2, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	defer func() { _ = d2.Close() }()

	if _, err := d2.GetCard(context.Background(), "8988211000000000017"); err != nil {
		t.Fatalf("GetCard after reopen: %v", err)
	}
}

func TestCardCRUD(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	c := card("8988211000000000017", 1, 2)
	c.KeySets[1].Algorithm, c.KeySets[1].CCLength = "aes", 4

	if err := d.CreateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	if err := d.CreateCard(ctx, c); !errors.Is(err, db.ErrAlreadyExists) {
		t.Fatalf("duplicate = %v, want ErrAlreadyExists", err)
	}

	got, err := d.GetCard(ctx, c.ICCID)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.KeySets) != 2 || !bytes.Equal(got.KeySets[0].KIc, c.KeySets[0].KIc) ||
		got.KeySets[0].CCLength != 8 || got.KeySets[1].CCLength != 4 || got.KeySets[0].Counter != 0 {
		t.Fatalf("key sets = %+v", got.KeySets)
	}

	got.MSISDN, got.Description, got.KVN = "+15550000001", "A56", 2
	if err := d.UpdateCard(ctx, got); err != nil {
		t.Fatal(err)
	}

	got, _ = d.GetCard(ctx, c.ICCID)
	if got.MSISDN != "+15550000001" || got.Description != "A56" || got.KVN != 2 || len(got.KeySets) != 2 {
		t.Fatalf("updated = %+v", got)
	}

	if err := d.UpdateCard(ctx, card("8988211000000000025", 1)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("update missing = %v, want ErrNotFound", err)
	}

	if err := d.DeleteCard(ctx, c.ICCID); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteCard(ctx, c.ICCID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("delete again = %v, want ErrNotFound", err)
	}

	// The key sets went with the card: recreating it with one key set leaves no other.
	if err := d.CreateCard(ctx, card(c.ICCID, 3)); err != nil {
		t.Fatal(err)
	}

	got, _ = d.GetCard(ctx, c.ICCID)
	if len(got.KeySets) != 1 || got.KeySets[0].Version != 3 {
		t.Fatalf("key sets after recreate = %+v", got.KeySets)
	}
}

func TestImportCards(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	existing := card("8988211000000000017", 1)
	existing.MSISDN, existing.Description, existing.TAR = "+15550000001", "A56", []byte{0xb0, 0x00, 0x01}

	if err := d.CreateCard(ctx, existing); err != nil {
		t.Fatal(err)
	}

	imported := card("8988211000000000017", 1, 2)
	imported.IMSI = "999700000000001"
	imported.TAR = []byte{0xb0, 0x00, 0x10}

	created, updated, err := d.ImportCards(ctx, []db.Card{imported, card("8988211000000000025", 1)})
	if err != nil {
		t.Fatal(err)
	}

	if created != 1 || updated != 1 {
		t.Fatalf("created %d, updated %d", created, updated)
	}

	got, _ := d.GetCard(ctx, existing.ICCID)
	if got.IMSI != "999700000000001" || got.MSISDN != "+15550000001" || got.Description != "A56" ||
		!bytes.Equal(got.TAR, []byte{0xb0, 0x00, 0x01}) || len(got.KeySets) != 2 {
		t.Fatalf("imported over = %+v", got)
	}

	cards, total, err := d.ListCards(ctx, 1, 25)
	if err != nil {
		t.Fatal(err)
	}

	if total != 2 || len(cards) != 2 || cards[0].ICCID != "8988211000000000017" || len(cards[1].KeySets) != 1 {
		t.Fatalf("cards = %+v, total %d", cards, total)
	}

	// The second page of one holds the second card, with its own key sets only.
	cards, total, err = d.ListCards(ctx, 2, 1)
	if err != nil || total != 2 || len(cards) != 1 || cards[0].ICCID != "8988211000000000025" || len(cards[0].KeySets) != 1 {
		t.Fatalf("page 2 = %+v, total %d, %v", cards, total, err)
	}

	if cards, _, _ := d.ListCards(ctx, 3, 1); len(cards) != 0 {
		t.Fatalf("page 3 = %+v", cards)
	}
}

func TestImportCardsIsAtomic(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	bad := card("8988211000000000025", 1)
	bad.KeySets[0].Algorithm = "des"

	if _, _, err := d.ImportCards(ctx, []db.Card{card("8988211000000000017", 1), bad}); err == nil {
		t.Fatal("expected an error for a key set the schema refuses")
	}

	if cards, _, _ := d.ListCards(ctx, 1, 25); len(cards) != 0 {
		t.Fatalf("a failed import left %d cards", len(cards))
	}
}

func TestNextCounter(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if err := d.CreateCard(ctx, card("8988211000000000017", 1, 2)); err != nil {
		t.Fatal(err)
	}

	for want := uint64(1); want <= 3; want++ {
		got, err := d.NextCounter(ctx, "8988211000000000017", 1)
		if err != nil || got != want {
			t.Fatalf("counter = %d, %v, want %d", got, err, want)
		}
	}

	// Each key set has its own counter (TS 102 225 annex A).
	if got, _ := d.NextCounter(ctx, "8988211000000000017", 2); got != 1 {
		t.Fatalf("key set 2 counter = %d", got)
	}

	if _, err := d.NextCounter(ctx, "8988211000000000025", 1); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing card = %v", err)
	}

	if _, err := d.NextCounter(ctx, "8988211000000000017", 9); !errors.Is(err, db.ErrCounterExhausted) {
		t.Fatalf("missing key set = %v", err)
	}

	// A re-import with the same key set versions keeps their counters: they are the card's.
	if _, _, err := d.ImportCards(ctx, []db.Card{card("8988211000000000017", 1, 2)}); err != nil {
		t.Fatal(err)
	}

	if got, _ := d.NextCounter(ctx, "8988211000000000017", 1); got != 4 {
		t.Fatalf("counter after re-import = %d, want 4", got)
	}
}

func TestCounterExhausted(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	c := card("8988211000000000017", 1)
	c.KeySets[0].Counter = 1<<40 - 1

	if err := d.CreateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	if _, err := d.NextCounter(ctx, c.ICCID, 1); !errors.Is(err, db.ErrCounterExhausted) {
		t.Fatalf("err = %v, want ErrCounterExhausted", err)
	}
}

// TestImportKeepsUsableKVN checks that a re-import without the card's selected key set moves it to the file's first.
func TestImportKeepsUsableKVN(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	c := card("8988211000000000017", 3, 1)
	if err := d.CreateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	if _, _, err := d.ImportCards(ctx, []db.Card{card(c.ICCID, 1, 2)}); err != nil {
		t.Fatal(err)
	}

	if got, _ := d.GetCard(ctx, c.ICCID); got.KVN != 1 || len(got.KeySets) != 2 {
		t.Fatalf("card = %+v", got)
	}
}

// TestJobSteps checks a job's life: created with its card's TAR and KVN, sent, advanced to its next command, finished.
func TestJobSteps(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	c := card("8988211000000000017", 2)
	c.MSISDN = "+15550000017"

	if err := d.CreateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	ids, err := d.CreateJobs(ctx, []db.Job{{ICCID: c.ICCID, Operation: db.OperationOPLMNRead}})
	if err != nil {
		t.Fatal(err)
	}

	j, _ := d.GetJob(ctx, ids[0])
	if !bytes.Equal(j.TAR, c.TAR) || j.KVN != 2 || j.Status != db.JobQueued || j.Step != 0 || j.FileSize != -1 || j.PoRStatus != -1 {
		t.Fatalf("created = %+v", j)
	}

	// A change of the card's key set does not change the job's.
	c.KVN = 2

	c.TAR = []byte{0xb0, 0x00, 0x20}
	if err := d.UpdateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	if _, err := d.SentJob(ctx, "55"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("sent job before it is sent = %v", err)
	}

	if err := d.MarkJobSent(ctx, j.ID, 7, "55", []byte{1}); err != nil {
		t.Fatal(err)
	}

	j, _ = d.GetJob(ctx, j.ID)
	if j.Status != db.JobSent || j.Counter != 7 || j.MessageID != "55" || !bytes.Equal(j.TAR, []byte{0xb0, 0x00, 0x10}) {
		t.Fatalf("sent = %+v", j)
	}

	// The SMSC's deliver_sm finds the job by its message_id, or by the card's MSISDN.
	if got, err := d.SentJob(ctx, "55"); err != nil || got.ID != j.ID {
		t.Fatalf("SentJob = %+v, %v", got, err)
	}

	if got, err := d.SentJobTo(ctx, c.MSISDN); err != nil || got.ID != j.ID {
		t.Fatalf("SentJobTo(%s) = %+v, %v", c.MSISDN, got, err)
	}

	if _, err := d.SentJobTo(ctx, "+15559999999"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("SentJobTo another number = %v", err)
	}

	j.Step, j.FileSize, j.PoRStatus, j.StatusWord, j.Response = 1, 60, 0, 0x9000, []byte{2}
	if err := d.AdvanceJob(ctx, j); err != nil {
		t.Fatal(err)
	}

	if err := d.AdvanceJob(ctx, j); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("advance a queued job = %v", err)
	}

	j, _ = d.GetJob(ctx, j.ID)
	if j.Status != db.JobQueued || j.Step != 1 || j.FileSize != 60 || j.StatusWord != 0x9000 {
		t.Fatalf("advanced = %+v", j)
	}

	j.Status, j.Error = db.JobUnconfirmed, "no PoR"
	if err := d.FinishJob(ctx, j); err != nil {
		t.Fatal(err)
	}

	j, _ = d.GetJob(ctx, j.ID)
	if j.Status != db.JobUnconfirmed || j.Error != "no PoR" {
		t.Fatalf("finished = %+v", j)
	}

	if _, err := d.CreateJobs(ctx, []db.Job{{ICCID: "8988211000000000025", Operation: db.OperationOPLMNRead}}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("job for a missing card = %v", err)
	}
}

func TestSMSC(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if _, err := d.GetSMSC(ctx); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("unset SMSC = %v, want ErrNotFound", err)
	}

	want := db.SMSC{
		Host: "127.0.0.1", Port: 2775, SystemID: "simota", Originator: "+15550001000", Validity: time.Hour,
	}
	if err := d.PutSMSC(ctx, want); err == nil {
		t.Fatal("expected a first SMSC without a password to be refused")
	}

	want.Password = "secret"
	if err := d.PutSMSC(ctx, want); err != nil {
		t.Fatal(err)
	}

	// An update without a password keeps it.
	want.Host, want.Port, want.TLS, want.Validity = "smsc.example", 3550, true, 24*time.Hour

	update := want
	update.Password = ""

	if err := d.PutSMSC(ctx, update); err != nil {
		t.Fatal(err)
	}

	if got, err := d.GetSMSC(ctx); err != nil || got != want {
		t.Fatalf("SMSC = %+v, %v, want %+v", got, err, want)
	}

	// The schema refuses what SMPP cannot carry: a system_id of 16 characters, a password of 9 (SMPP 3.4 section
	// 4.1.1, table 4-1).
	for _, bad := range []db.SMSC{
		{Host: "h", Port: 2775, SystemID: "0123456789abcdef", Password: "pw", Validity: time.Hour},
		{Host: "h", Port: 2775, SystemID: "simota", Password: "123456789", Validity: time.Hour},
		{Host: "h", Port: 0, SystemID: "simota", Password: "pw", Validity: time.Hour},
	} {
		if err := d.PutSMSC(ctx, bad); err == nil {
			t.Errorf("PutSMSC(%+v) accepted", bad)
		}
	}
}
