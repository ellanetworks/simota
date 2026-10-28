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

var migrations = []string{
	`CREATE TABLE cards (
		iccid TEXT PRIMARY KEY,
		imsi TEXT NOT NULL,
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
		kik BLOB,
		PRIMARY KEY (iccid, version)
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
