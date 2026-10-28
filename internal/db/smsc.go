package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SMSC is the Ella SMSC that the OTA messages go through, over SMPP 3.4: its host and port, whether the connection
// is over TLS, the system_id and password that simota binds with, the address the messages come from, and how long
// the SMSC attempts their delivery.
type SMSC struct {
	Host       string
	Port       int
	TLS        bool
	SystemID   string
	Password   string
	Originator string
	Validity   time.Duration
}

// GetSMSC is the SMSC, or ErrNotFound before one is set.
func (d *DB) GetSMSC(ctx context.Context) (SMSC, error) {
	var (
		s        SMSC
		validity int64
	)

	err := d.conn.QueryRowContext(ctx, `SELECT host, port, tls, system_id, password, originator, validity FROM smsc
		WHERE id = 1`).Scan(&s.Host, &s.Port, &s.TLS, &s.SystemID, &s.Password, &s.Originator, &validity)
	if errors.Is(err, sql.ErrNoRows) {
		return SMSC{}, ErrNotFound
	}

	if err != nil {
		return SMSC{}, fmt.Errorf("get smsc: %w", err)
	}

	s.Validity = time.Duration(validity)

	return s, nil
}

// PutSMSC sets the SMSC. An empty password keeps the one set.
func (d *DB) PutSMSC(ctx context.Context, s SMSC) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if s.Password == "" {
			err := tx.QueryRowContext(ctx, "SELECT password FROM smsc WHERE id = 1").Scan(&s.Password)
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("put smsc: a password is required")
			}

			if err != nil {
				return err
			}
		}

		_, err := tx.ExecContext(ctx, `INSERT INTO smsc (id, host, port, tls, system_id, password, originator, validity)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET host = excluded.host, port = excluded.port, tls = excluded.tls,
			system_id = excluded.system_id, password = excluded.password, originator = excluded.originator,
			validity = excluded.validity`, s.Host, s.Port, s.TLS, s.SystemID, s.Password, s.Originator, int64(s.Validity))

		return err
	})
}
