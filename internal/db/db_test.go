package db_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
		c.KeySets = append(c.KeySets, db.KeySet{Version: v, Algorithm: "3des", KIc: bytes.Repeat([]byte{byte(v)}, 16),
			KID: bytes.Repeat([]byte{byte(v + 0x10)}, 16)})
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
	c.KeySets[0].KIK = bytes.Repeat([]byte{0x33}, 16)

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
		!bytes.Equal(got.KeySets[0].KIK, c.KeySets[0].KIK) || got.KeySets[1].KIK != nil {
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

	cards, err := d.ListCards(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(cards) != 2 || cards[0].ICCID != "8988211000000000017" || len(cards[1].KeySets) != 1 {
		t.Fatalf("cards = %+v", cards)
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

	if cards, _ := d.ListCards(ctx); len(cards) != 0 {
		t.Fatalf("a failed import left %d cards", len(cards))
	}
}
