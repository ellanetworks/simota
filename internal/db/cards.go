package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Card is a SIM card, the MSISDN it is reached at, and the TAR and key set that OTA commands to it use.
type Card struct {
	ICCID       string
	IMSI        string
	MSISDN      string
	Description string
	// TAR is that of the card's ADF RFM application linked to the USIM (TS 102 226 clause 7.3).
	TAR []byte
	// KVN is the version of the key set that OTA commands use.
	KVN       int
	KeySets   []KeySet
	CreatedAt time.Time
	UpdatedAt time.Time
}

// KeySet is an SCP80 key set of a card's security domain.
type KeySet struct {
	Version int
	// Algorithm is "3des" or "aes".
	Algorithm string
	KIc       []byte
	KID       []byte
	// CCLength is the length of the key set's cryptographic checksums: 8, or 4 for an AES key set configured so
	// (TS 102 225 clause 5.1.3.1).
	CCLength int
	// Counter is the last CNTR sent with the key set. It is the card's state, kept when the keys are replaced.
	Counter uint64
}

// maxCounter is the largest CNTR, 5 octets (TS 102 225 clause 5.1).
const maxCounter = 1<<40 - 1

// ErrCounterExhausted is a key set whose counter reached its maximum: the card blocks it (TS 102 225 clause 5.1.4).
var ErrCounterExhausted = errors.New("key set counter exhausted")

// CreateCard stores a new card with its key sets.
func (d *DB) CreateCard(ctx context.Context, c Card) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		now := d.now().UnixMilli()

		_, err := tx.ExecContext(ctx, `INSERT INTO cards (iccid, imsi, msisdn, description, tar, kvn, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.ICCID, c.IMSI, c.MSISDN, c.Description, c.TAR, c.KVN, now, now)
		if isUnique(err) {
			return ErrAlreadyExists
		}

		if err != nil {
			return err
		}

		return putKeySets(ctx, tx, c.ICCID, c.KeySets)
	})
}

// NextCounter takes the next counter of a card's key set: one more than the last one sent, stored before it is used,
// so that no two commands share a counter.
func (d *DB) NextCounter(ctx context.Context, iccid string, version int) (uint64, error) {
	var counter uint64

	err := d.conn.QueryRowContext(ctx, `UPDATE key_sets SET counter = counter + 1 WHERE iccid = ? AND version = ?
		AND counter < ? RETURNING counter`, iccid, version, maxCounter).Scan(&counter)
	if errors.Is(err, sql.ErrNoRows) {
		if _, gerr := d.GetCard(ctx, iccid); gerr != nil {
			return 0, gerr
		}

		return 0, ErrCounterExhausted
	}

	if err != nil {
		return 0, fmt.Errorf("next counter: %w", err)
	}

	return counter, nil
}

// UpdateCard updates the settings of a card, not its key sets.
func (d *DB) UpdateCard(ctx context.Context, c Card) error {
	res, err := d.conn.ExecContext(ctx, `UPDATE cards SET imsi = ?, msisdn = ?, description = ?, tar = ?, kvn = ?,
		updated_at = ? WHERE iccid = ?`, c.IMSI, c.MSISDN, c.Description, c.TAR, c.KVN, d.now().UnixMilli(), c.ICCID)
	if err != nil {
		return fmt.Errorf("update card: %w", err)
	}

	return affected(res)
}

func (d *DB) DeleteCard(ctx context.Context, iccid string) error {
	res, err := d.conn.ExecContext(ctx, "DELETE FROM cards WHERE iccid = ?", iccid)
	if err != nil {
		return fmt.Errorf("delete card: %w", err)
	}

	return affected(res)
}

func (d *DB) GetCard(ctx context.Context, iccid string) (Card, error) {
	cards, err := d.cards(ctx, "WHERE iccid = ?", iccid)
	if err != nil {
		return Card{}, err
	}

	if len(cards) == 0 {
		return Card{}, ErrNotFound
	}

	return cards[0], nil
}

// ListCards lists a page, from 1, of the cards by ICCID, and the number of cards.
func (d *DB) ListCards(ctx context.Context, page, perPage int) ([]Card, int, error) {
	var total int
	if err := d.conn.QueryRowContext(ctx, "SELECT count(*) FROM cards").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count cards: %w", err)
	}

	cards, err := d.cards(ctx, "ORDER BY iccid LIMIT ? OFFSET ?", perPage, (page-1)*perPage)

	return cards, total, err
}

// ImportCards stores cards from a vendor's key file: a new card is created, and an existing card gets the IMSI and
// key sets of the file, keeping its MSISDN, description, TAR, counters, and key set choice when the file still has
// that key set; otherwise its first one.
func (d *DB) ImportCards(ctx context.Context, cards []Card) (created, updated int, err error) {
	err = d.tx(ctx, func(tx *sql.Tx) error {
		now := d.now().UnixMilli()

		for _, c := range cards {
			res, err := tx.ExecContext(ctx, "UPDATE cards SET imsi = ?, updated_at = ? WHERE iccid = ?", c.IMSI, now, c.ICCID)
			if err != nil {
				return err
			}

			if n, _ := res.RowsAffected(); n == 1 {
				updated++
			} else {
				if _, err := tx.ExecContext(ctx, `INSERT INTO cards (iccid, imsi, msisdn, description, tar, kvn, created_at,
					updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.ICCID, c.IMSI, c.MSISDN, c.Description, c.TAR, c.KVN, now,
					now); err != nil {
					return err
				}

				created++
			}

			if err := putKeySets(ctx, tx, c.ICCID, c.KeySets); err != nil {
				return err
			}

			if _, err := tx.ExecContext(ctx, `UPDATE cards SET kvn = (SELECT min(version) FROM key_sets WHERE iccid = ?)
				WHERE iccid = ? AND kvn NOT IN (SELECT version FROM key_sets WHERE iccid = ?)`, c.ICCID, c.ICCID, c.ICCID); err != nil {
				return err
			}
		}

		return nil
	})

	return created, updated, err
}

// putKeySets replaces the key sets of a card. A key set version that stays keeps its counter, which is the card's.
func putKeySets(ctx context.Context, tx *sql.Tx, iccid string, sets []KeySet) error {
	versions := make([]any, 0, len(sets)+1)
	versions = append(versions, iccid)

	for _, k := range sets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO key_sets (iccid, version, algorithm, kic, kid, cc_length, counter)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (iccid, version) DO UPDATE SET algorithm = excluded.algorithm,
			kic = excluded.kic, kid = excluded.kid, cc_length = excluded.cc_length`,
			iccid, k.Version, k.Algorithm, k.KIc, k.KID, ccLengthOf(k), k.Counter); err != nil {
			return err
		}

		versions = append(versions, k.Version)
	}

	_, err := tx.ExecContext(ctx, "DELETE FROM key_sets WHERE iccid = ? AND version NOT IN (?"+
		strings.Repeat(", ?", len(sets)-1)+")", versions...)

	return err
}

func ccLengthOf(k KeySet) int {
	if k.CCLength == 0 {
		return 8
	}

	return k.CCLength
}

// cards are the cards that clause, the end of a SELECT from cards, selects, with their key sets.
func (d *DB) cards(ctx context.Context, clause string, args ...any) ([]Card, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT iccid, imsi, msisdn, description, tar, kvn, created_at, updated_at
		FROM cards `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}

	var (
		cards []Card
		index = map[string]int{}
	)

	for rows.Next() {
		var (
			c                    Card
			created, lastUpdated int64
		)

		if err := rows.Scan(&c.ICCID, &c.IMSI, &c.MSISDN, &c.Description, &c.TAR, &c.KVN, &created, &lastUpdated); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list cards: %w", err)
		}

		c.CreatedAt, c.UpdatedAt = time.UnixMilli(created), time.UnixMilli(lastUpdated)
		index[c.ICCID] = len(cards)
		cards = append(cards, c)
	}

	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}

	if len(cards) == 0 {
		return cards, nil
	}

	iccids := make([]any, 0, len(cards))
	for _, c := range cards {
		iccids = append(iccids, c.ICCID)
	}

	keyRows, err := d.conn.QueryContext(ctx, `SELECT iccid, version, algorithm, kic, kid, cc_length, counter FROM key_sets
		WHERE iccid IN (?`+strings.Repeat(", ?", len(iccids)-1)+`) ORDER BY iccid, version`, iccids...)
	if err != nil {
		return nil, fmt.Errorf("list key sets: %w", err)
	}

	defer func() { _ = keyRows.Close() }()

	for keyRows.Next() {
		var (
			iccid string
			k     KeySet
		)

		if err := keyRows.Scan(&iccid, &k.Version, &k.Algorithm, &k.KIc, &k.KID, &k.CCLength, &k.Counter); err != nil {
			return nil, fmt.Errorf("list key sets: %w", err)
		}

		if i, ok := index[iccid]; ok {
			cards[i].KeySets = append(cards[i].KeySets, k)
		}
	}

	return cards, keyRows.Err()
}

func (d *DB) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func affected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

// SetCounter sets the counter of a card's key set: the last CNTR the card accepted, when the card was used elsewhere.
// A card does not tell its counter (TS 102 225 clause 5.1.4).
func (d *DB) SetCounter(ctx context.Context, iccid string, version int, counter uint64) error {
	if counter > maxCounter {
		return fmt.Errorf("set counter: %d exceeds 5 octets", counter)
	}

	res, err := d.conn.ExecContext(ctx, "UPDATE key_sets SET counter = ? WHERE iccid = ? AND version = ?", counter, iccid, version)
	if err != nil {
		return fmt.Errorf("set counter: %w", err)
	}

	return affected(res)
}
