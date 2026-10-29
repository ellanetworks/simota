// Package db stores the cards and their OTA key sets in SQLite.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-sqlite3"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

func isUnique(err error) bool {
	var e sqlite3.Error

	return errors.As(err, &e) && (e.ExtendedCode == sqlite3.ErrConstraintUnique || e.ExtendedCode == sqlite3.ErrConstraintPrimaryKey)
}

type DB struct {
	conn *sql.DB
	now  func() time.Time
}

// migrations are the schema's versions; the first is the schema. A job's por_missing marks it closed without the PoR to
// its last command, by a receipt or the deadline, which a later PoR of that command's TAR and counter still decides;
// its attempts count the failed submits of its current command. An IMSI write keeps the IMSI it writes and the card's
// IMSI on record that it replaces.
var migrations = []string{
	`CREATE TABLE cards (
		iccid TEXT PRIMARY KEY,
		imsi TEXT,
		msisdn TEXT NOT NULL,
		description TEXT NOT NULL,
		tar BLOB NOT NULL CHECK (length(tar) = 3),
		kvn INTEGER NOT NULL CHECK (kvn BETWEEN 1 AND 15),
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE key_sets (
		iccid TEXT NOT NULL REFERENCES cards (iccid) ON DELETE CASCADE,
		version INTEGER NOT NULL CHECK (version BETWEEN 1 AND 15),
		algorithm TEXT NOT NULL CHECK (algorithm IN ('3des', 'aes')),
		kic BLOB NOT NULL,
		kid BLOB NOT NULL,
		cc_length INTEGER NOT NULL CHECK (cc_length IN (4, 8)),
		counter INTEGER NOT NULL CHECK (counter BETWEEN 0 AND 1099511627775),
		PRIMARY KEY (iccid, version),
		CHECK (algorithm = 'aes' OR cc_length = 8)
	);
	CREATE TABLE jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		iccid TEXT NOT NULL REFERENCES cards (iccid) ON DELETE CASCADE,
		operation TEXT NOT NULL CHECK (operation IN ('oplmn_read', 'oplmn_set', 'oplmn_clear', 'imsi_write')),
		slot INTEGER CHECK (slot BETWEEN 1 AND 51),
		mcc TEXT,
		mnc TEXT,
		access INTEGER CHECK (access BETWEEN 0 AND 65535),
		imsi TEXT,
		previous_imsi TEXT,
		tar BLOB NOT NULL CHECK (length(tar) = 3),
		kvn INTEGER NOT NULL CHECK (kvn BETWEEN 1 AND 15),
		status TEXT NOT NULL CHECK (status IN ('queued', 'sent', 'succeeded', 'unconfirmed', 'failed')),
		step INTEGER NOT NULL CHECK (step >= 0),
		file_size INTEGER CHECK (file_size BETWEEN 0 AND 255),
		counter INTEGER,
		message_id TEXT,
		command BLOB,
		response BLOB,
		por_status INTEGER,
		status_word INTEGER,
		warning INTEGER NOT NULL CHECK (warning IN (0, 1)),
		data BLOB,
		error TEXT NOT NULL,
		por_missing INTEGER NOT NULL CHECK (por_missing IN (0, 1)),
		attempts INTEGER NOT NULL CHECK (attempts >= 0),
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		CHECK (operation NOT IN ('oplmn_set', 'oplmn_clear') OR slot IS NOT NULL),
		CHECK (operation != 'oplmn_set' OR mcc IS NOT NULL AND mnc IS NOT NULL AND access IS NOT NULL),
		CHECK (operation != 'imsi_write' OR imsi IS NOT NULL AND previous_imsi IS NOT NULL),
		CHECK (status != 'sent' OR command IS NOT NULL AND counter IS NOT NULL AND message_id IS NOT NULL)
	);
	CREATE INDEX jobs_iccid ON jobs (iccid, id);
	CREATE INDEX jobs_status ON jobs (status, id);
	CREATE INDEX jobs_message_id ON jobs (message_id) WHERE status = 'sent';
	CREATE TABLE smsc (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		host TEXT NOT NULL CHECK (host != ''),
		port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
		tls INTEGER NOT NULL CHECK (tls IN (0, 1)),
		system_id TEXT NOT NULL CHECK (length(system_id) BETWEEN 1 AND 15),
		password TEXT NOT NULL CHECK (length(password) BETWEEN 1 AND 8),
		originator TEXT NOT NULL,
		validity INTEGER NOT NULL CHECK (validity > 0)
	);`,
}

// Open opens the database at path, creating it readable and writable by its owner only: it holds the cards' keys.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := restrict(path); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	d := &DB{conn: conn, now: time.Now}
	if err := d.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return d, nil
}

// Ping tells whether the database answers.
func (d *DB) Ping(ctx context.Context) error {
	return d.conn.PingContext(ctx)
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) migrate(ctx context.Context) error {
	var version int
	if err := d.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := d.conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}

	return nil
}

// restrict creates the database file if absent, readable and writable by its owner only, and makes an existing one
// so. SQLite gives its journal files the permissions of the database file.
func restrict(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Chmod(path, 0o600)
}
