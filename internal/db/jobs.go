package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Operation is what a job does on a card's EF.OPLMNwAcT.
type Operation string

const (
	OperationOPLMNRead  Operation = "oplmn_read"
	OperationOPLMNSet   Operation = "oplmn_set"
	OperationOPLMNClear Operation = "oplmn_clear"
)

// JobStatus is where a job is: queued for its next command, sent to the SMSC and awaiting the card's answer, or done.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobSent      JobStatus = "sent"
	JobSucceeded JobStatus = "succeeded"
	// JobUnconfirmed is a job whose command may have taken effect, without a PoR that tells.
	JobUnconfirmed JobStatus = "unconfirmed"
	JobFailed      JobStatus = "failed"
)

// Job is an OTA operation on a card, run as one or more commands, and its outcome.
type Job struct {
	ID        int64
	ICCID     string
	Operation Operation
	// Slot is the entry, from 1, that a set or clear writes. MCC, MNC and Access are the entry a set writes.
	Slot     int
	MCC, MNC string
	Access   int
	// TAR and KVN are the card's when the job was created: its commands and PoRs use them.
	TAR    []byte
	KVN    int
	Status JobStatus
	// Step is the index of the job's current command, and FileSize the size of the file, -1 until read.
	Step     int
	FileSize int
	// Counter and MessageID are the CNTR and the SMSC's message_id of the current command, once sent.
	Counter   uint64
	MessageID string
	// Command is the TP-UDH and TP-UD of the current Command Packet, and Response those of its Response Packet.
	Command, Response []byte
	// PoRStatus and StatusWord are the Response Status Code and the status word of the last command answered, or -1.
	PoRStatus  int
	StatusWord int
	// Warning is a last status word that completed with a warning.
	Warning bool
	// Data is the file contents the job read.
	Data []byte
	// Error tells why a job failed or is unconfirmed.
	Error string
	// PoRMissing is a job closed without the PoR to its last command, by a delivery receipt or the deadline: a PoR
	// that comes later, with the command's TAR and counter, still gives its outcome.
	PoRMissing bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ErrBusy is a job for a card that has one queued or sent already.
var ErrBusy = errors.New("card has a job in progress")

// CreateJobs queues jobs, all or none, with the TAR and key set version of their card. A card may have one job in
// progress at a time.
func (d *DB) CreateJobs(ctx context.Context, jobs []Job) ([]int64, error) {
	ids := make([]int64, 0, len(jobs))

	err := d.tx(ctx, func(tx *sql.Tx) error {
		now := d.now().UnixMilli()

		for _, j := range jobs {
			var busy int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE iccid = ? AND status IN ('queued', 'sent')",
				j.ICCID).Scan(&busy); err != nil {
				return err
			}

			if busy > 0 {
				return fmt.Errorf("%w: %s", ErrBusy, j.ICCID)
			}

			res, err := tx.ExecContext(ctx, `INSERT INTO jobs (iccid, operation, slot, mcc, mnc, access, tar, kvn, status, step,
				warning, error, por_missing, attempts, created_at, updated_at)
				SELECT iccid, ?, ?, ?, ?, ?, tar, kvn, 'queued', 0, 0, '', 0, 0, ?, ?
				FROM cards WHERE iccid = ?`, j.Operation, nullInt(j.Slot), nullString(j.MCC), nullString(j.MNC), nullAccess(j),
				now, now, j.ICCID)
			if err != nil {
				return err
			}

			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("card %s: %w", j.ICCID, ErrNotFound)
			}

			id, err := res.LastInsertId()
			if err != nil {
				return err
			}

			ids = append(ids, id)
		}

		return nil
	})

	return ids, err
}

func (d *DB) GetJob(ctx context.Context, id int64) (Job, error) {
	jobs, err := d.jobs(ctx, "WHERE id = ?", id)
	if err != nil {
		return Job{}, err
	}

	if len(jobs) == 0 {
		return Job{}, ErrNotFound
	}

	return jobs[0], nil
}

// ListJobs lists a page, from 1, of the jobs, newest first, of a card or of all when iccid is empty, and their number.
func (d *DB) ListJobs(ctx context.Context, iccid string, page, perPage int) ([]Job, int, error) {
	where, args := "", []any{}
	if iccid != "" {
		where, args = "WHERE iccid = ?", []any{iccid}
	}

	var total int
	if err := d.conn.QueryRowContext(ctx, "SELECT count(*) FROM jobs "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	jobs, err := d.jobs(ctx, where+" ORDER BY id DESC LIMIT ? OFFSET ?", append(args, perPage, (page-1)*perPage)...)

	return jobs, total, err
}

// JobsIn lists the jobs in a status, oldest first.
func (d *DB) JobsIn(ctx context.Context, status JobStatus, limit int) ([]Job, error) {
	return d.jobs(ctx, "WHERE status = ? ORDER BY id LIMIT ?", status, limit)
}

// SentJob is the job whose current command went to the SMSC as message messageID, or ErrNotFound.
func (d *DB) SentJob(ctx context.Context, messageID string) (Job, error) {
	return d.oneJob(ctx, "WHERE status = 'sent' AND message_id = ?", messageID)
}

// maxAwaiting is the most jobs that AwaitingPoR returns.
const maxAwaiting = 10

// AwaitingPoR lists, newest first, the jobs of the cards of MSISDN msisdn whose last command, of TAR tar, a PoR may
// answer: those sent, and those closed without its PoR. The PoR's counter tells which, if any, it answers.
func (d *DB) AwaitingPoR(ctx context.Context, msisdn string, tar []byte) ([]Job, error) {
	return d.jobs(ctx, `WHERE tar = ? AND (status = 'sent' OR por_missing = 1)
		AND iccid IN (SELECT iccid FROM cards WHERE msisdn = ?) ORDER BY id DESC LIMIT ?`, tar, msisdn, maxAwaiting)
}

func (d *DB) oneJob(ctx context.Context, clause string, args ...any) (Job, error) {
	jobs, err := d.jobs(ctx, clause+" LIMIT 1", args...)
	if err != nil {
		return Job{}, err
	}

	if len(jobs) == 0 {
		return Job{}, ErrNotFound
	}

	return jobs[0], nil
}

// MarkJobSent records that a queued job's current command, with its counter, went to the SMSC as message messageID.
func (d *DB) MarkJobSent(ctx context.Context, id int64, counter uint64, messageID string, command []byte) error {
	res, err := d.conn.ExecContext(ctx, `UPDATE jobs SET status = 'sent', counter = ?, message_id = ?, command = ?,
		response = NULL, attempts = 0, updated_at = ? WHERE id = ? AND status = 'queued'`, counter, messageID, command,
		d.now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("mark job sent: %w", err)
	}

	return affected(res)
}

// AdvanceJob records the answer to a sent job's command, and queues its next command.
func (d *DB) AdvanceJob(ctx context.Context, j Job) error {
	res, err := d.conn.ExecContext(ctx, `UPDATE jobs SET status = 'queued', step = ?, file_size = ?, response = ?,
		por_status = ?, status_word = ?, warning = ?, data = ?, updated_at = ? WHERE id = ? AND status = 'sent'`,
		j.Step, nullUnknown(j.FileSize), j.Response, nullUnknown(j.PoRStatus), nullUnknown(j.StatusWord), j.Warning, j.Data,
		d.now().UnixMilli(), j.ID)
	if err != nil {
		return fmt.Errorf("advance job: %w", err)
	}

	return affected(res)
}

// CountAttempt counts a failed submit of a queued job's current command, and returns how many there were.
func (d *DB) CountAttempt(ctx context.Context, id int64) (int, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx, `UPDATE jobs SET attempts = attempts + 1 WHERE id = ? AND status = 'queued'
		RETURNING attempts`, id).Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}

		return 0, fmt.Errorf("count attempt: %w", err)
	}

	return n, nil
}

// FinishJob records the outcome of a job in progress: its final status, the answer it got, if any, the reason for a
// failed or unconfirmed one, and whether it lacks the PoR to its last command.
func (d *DB) FinishJob(ctx context.Context, j Job) error {
	return d.finish(ctx, j, "status IN ('queued', 'sent')")
}

// ReviseJob replaces the outcome of a job closed without the PoR to its last command, of counter j.Counter, with the
// one that this PoR gives.
func (d *DB) ReviseJob(ctx context.Context, j Job) error {
	return d.finish(ctx, j, "por_missing = 1 AND counter = ?", int64(j.Counter))
}

func (d *DB) finish(ctx context.Context, j Job, where string, args ...any) error {
	if j.Status != JobSucceeded && j.Status != JobUnconfirmed && j.Status != JobFailed {
		return fmt.Errorf("finish job: status %q is not final", j.Status)
	}

	res, err := d.conn.ExecContext(ctx, `UPDATE jobs SET status = ?, file_size = ?, response = ?, por_status = ?,
		status_word = ?, warning = ?, data = ?, error = ?, por_missing = ?, updated_at = ? WHERE id = ? AND `+where,
		append([]any{
			j.Status, nullUnknown(j.FileSize), j.Response, nullUnknown(j.PoRStatus), nullUnknown(j.StatusWord), j.Warning,
			j.Data, j.Error, j.PoRMissing, d.now().UnixMilli(), j.ID,
		}, args...)...)
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}

	return affected(res)
}

func (d *DB) jobs(ctx context.Context, clause string, args ...any) ([]Job, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, iccid, operation, slot, mcc, mnc, access, tar, kvn, status, step,
		file_size, counter, message_id, command, response, por_status, status_word, warning, data, error, por_missing,
		created_at, updated_at FROM jobs `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var jobs []Job

	for rows.Next() {
		var (
			j                              Job
			slot, access, fileSize         sql.NullInt64
			counter, porStatus, statusWord sql.NullInt64
			mcc, mnc, messageID            sql.NullString
			created, lastUpdated           int64
		)

		if err := rows.Scan(&j.ID, &j.ICCID, &j.Operation, &slot, &mcc, &mnc, &access, &j.TAR, &j.KVN, &j.Status, &j.Step,
			&fileSize, &counter, &messageID, &j.Command, &j.Response, &porStatus, &statusWord, &j.Warning, &j.Data, &j.Error,
			&j.PoRMissing, &created, &lastUpdated); err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}

		j.Slot, j.MCC, j.MNC, j.Access, j.MessageID = int(slot.Int64), mcc.String, mnc.String, int(access.Int64), messageID.String
		j.Counter = uint64(counter.Int64)
		j.FileSize, j.PoRStatus, j.StatusWord = unknown(fileSize), unknown(porStatus), unknown(statusWord)
		j.CreatedAt, j.UpdatedAt = time.UnixMilli(created), time.UnixMilli(lastUpdated)

		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

// unknown is n, or -1 for NULL.
func unknown(n sql.NullInt64) int {
	if !n.Valid {
		return -1
	}

	return int(n.Int64)
}

// nullInt is n, or NULL for zero.
func nullInt(n int) any {
	if n == 0 {
		return nil
	}

	return n
}

// nullUnknown is n, or NULL for -1, unknown.
func nullUnknown(n int) any {
	if n < 0 {
		return nil
	}

	return n
}

func nullString(s string) any {
	if s == "" {
		return nil
	}

	return s
}

// nullAccess is the access technology of a set, which may be zero, or NULL for the other operations.
func nullAccess(j Job) any {
	if j.Operation != OperationOPLMNSet {
		return nil
	}

	return j.Access
}
