package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Card is a SIM card, the MSISDN it is reached at, and the TAR and key set that OTA commands to it use.
type Card struct {
	ICCID       string
	IMSI        string
	MSISDN      string
	Description string
	TAR         []byte
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
	// KIK is nil when unknown.
	KIK []byte
}

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

// ListCards lists the cards by ICCID.
func (d *DB) ListCards(ctx context.Context) ([]Card, error) {
	return d.cards(ctx, "")
}

// ImportCards stores cards from a vendor's key file: a new card is created, and an existing card gets the IMSI and
// key sets of the file, keeping its MSISDN, description, TAR and key set choice.
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
		}

		return nil
	})

	return created, updated, err
}

// putKeySets replaces the key sets of a card.
func putKeySets(ctx context.Context, tx *sql.Tx, iccid string, sets []KeySet) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM key_sets WHERE iccid = ?", iccid); err != nil {
		return err
	}

	for _, k := range sets {
		if _, err := tx.ExecContext(ctx, "INSERT INTO key_sets (iccid, version, algorithm, kic, kid, kik) VALUES (?, ?, ?, ?, ?, ?)",
			iccid, k.Version, k.Algorithm, k.KIc, k.KID, k.KIK); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) cards(ctx context.Context, where string, args ...any) ([]Card, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT iccid, imsi, msisdn, description, tar, kvn, created_at, updated_at
		FROM cards `+where+` ORDER BY iccid`, args...)
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

	keyRows, err := d.conn.QueryContext(ctx, "SELECT iccid, version, algorithm, kic, kid, kik FROM key_sets ORDER BY iccid, version")
	if err != nil {
		return nil, fmt.Errorf("list key sets: %w", err)
	}

	defer func() { _ = keyRows.Close() }()

	for keyRows.Next() {
		var (
			iccid string
			k     KeySet
		)

		if err := keyRows.Scan(&iccid, &k.Version, &k.Algorithm, &k.KIc, &k.KID, &k.KIK); err != nil {
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
