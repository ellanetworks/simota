package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Operation string

const (
	OperationOPLMNRead  Operation = "oplmn_read"
	OperationOPLMNSet   Operation = "oplmn_set"
	OperationOPLMNClear Operation = "oplmn_clear"
	OperationIMSIWrite  Operation = "imsi_write"
)

type JobStatus string

const (
	JobQueued      JobStatus = "queued"
	JobSent        JobStatus = "sent"
	JobSucceeded   JobStatus = "succeeded"
	JobUnconfirmed JobStatus = "unconfirmed"
	JobFailed      JobStatus = "failed"
)

type Job struct {
	ID                              int64
	ICCID                           string
	Operation                       Operation
	Slot                            int
	MCC, MNC                        string
	Access                          int
	IMSI, PreviousIMSI              string
	TAR                             []byte
	KVN                             int
	Status                          JobStatus
	Step                            int
	FileSize                        int
	Counter                         uint64
	MessageID                       string
	Command, Response               []byte
	PoRStatus                       int
	StatusWord                      int
	Warning                         bool
	Data                            []byte
	Error                           string
	PoRMissing                      bool
	TraceParent, CommandTraceParent string
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
}

var ErrBusy = errors.New("card has a job in progress")

var ErrIMSIMismatch = errors.New("previous IMSI is not the card's IMSI on record")

func (d *DB) CreateJobs(ctx context.Context, jobs []Job) (_ []int64, err error) {
	traceparent := traceParent(ctx)

	ctx, span := d.start(ctx, opCreateJobs)

	defer func() { end(span, err) }()

	ids := make([]int64, 0, len(jobs))

	err = d.tx(ctx, func(tx *sql.Tx) error {
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

			if j.Operation == OperationIMSIWrite {
				var imsi sql.NullString

				err := tx.QueryRowContext(ctx, "SELECT imsi FROM cards WHERE iccid = ?", j.ICCID).Scan(&imsi)
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("card %s: %w", j.ICCID, ErrNotFound)
				}

				if err != nil {
					return err
				}

				if !imsi.Valid || imsi.String != j.PreviousIMSI {
					return fmt.Errorf("%w: %s", ErrIMSIMismatch, j.ICCID)
				}
			}

			res, err := tx.ExecContext(ctx, `INSERT INTO jobs (iccid, operation, slot, mcc, mnc, access, imsi, previous_imsi,
				tar, kvn, status, step, warning, error, por_missing, attempts, created_at, updated_at, traceparent)
				SELECT iccid, ?, ?, ?, ?, ?, ?, ?, tar, kvn, 'queued', 0, 0, '', 0, 0, ?, ?, ?
				FROM cards WHERE iccid = ?`, j.Operation, nullInt(j.Slot), nullString(j.MCC), nullString(j.MNC), nullAccess(j),
				nullString(j.IMSI), nullString(j.PreviousIMSI), now, now, traceparent, j.ICCID)
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

func (d *DB) GetJob(ctx context.Context, id int64) (_ Job, err error) {
	ctx, span := d.start(ctx, opGetJob)

	defer func() { end(span, err) }()

	jobs, err := d.jobs(ctx, "WHERE id = ?", id)
	if err != nil {
		return Job{}, err
	}

	if len(jobs) == 0 {
		return Job{}, ErrNotFound
	}

	return jobs[0], nil
}

func (d *DB) ListJobs(ctx context.Context, search string, page, perPage int) (_ []Job, _ int, err error) {
	ctx, span := d.start(ctx, opListJobs)

	defer func() { end(span, err) }()

	where, args := "", []any{}
	if search != "" {
		where = `WHERE instr(iccid, ?) > 0 OR iccid IN (SELECT iccid FROM cards WHERE instr(coalesce(imsi, ''), ?) > 0
			OR instr(msisdn, ?) > 0)`
		args = []any{search, search, search}
	}

	var total int
	if err := d.conn.QueryRowContext(ctx, "SELECT count(*) FROM jobs "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	jobs, err := d.jobs(ctx, where+" ORDER BY id DESC LIMIT ? OFFSET ?", append(args, perPage, (page-1)*perPage)...)

	return jobs, total, err
}

func (d *DB) JobsIn(ctx context.Context, status JobStatus, limit int) (_ []Job, err error) {
	ctx, span := d.start(ctx, opJobsIn)

	defer func() { end(span, err) }()

	return d.jobs(ctx, "WHERE status = ? ORDER BY id LIMIT ?", status, limit)
}

func (d *DB) SentJob(ctx context.Context, messageID string) (_ Job, err error) {
	ctx, span := d.start(ctx, opSentJob)

	defer func() { end(span, err) }()

	return d.oneJob(ctx, "WHERE status = 'sent' AND message_id = ?", messageID)
}

const maxAwaiting = 10

func (d *DB) AwaitingPoR(ctx context.Context, msisdn string, tar []byte) (_ []Job, err error) {
	ctx, span := d.start(ctx, opAwaitingPoR)

	defer func() { end(span, err) }()

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

func (d *DB) MarkJobSent(ctx context.Context, id int64, counter uint64, messageID string, command []byte) (err error) {
	traceparent := traceParent(ctx)

	ctx, span := d.start(ctx, opMarkJobSent)

	defer func() { end(span, err) }()

	res, err := d.conn.ExecContext(ctx, `UPDATE jobs SET status = 'sent', counter = ?, message_id = ?, command = ?,
		response = NULL, attempts = 0, command_traceparent = ?, updated_at = ? WHERE id = ? AND status = 'queued'`, counter,
		messageID, command, traceparent, d.now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("mark job sent: %w", err)
	}

	return affected(res)
}

func (d *DB) AdvanceJob(ctx context.Context, j Job) (err error) {
	ctx, span := d.start(ctx, opAdvanceJob)

	defer func() { end(span, err) }()

	res, err := d.conn.ExecContext(ctx, `UPDATE jobs SET status = 'queued', step = ?, file_size = ?, response = ?,
		por_status = ?, status_word = ?, warning = ?, data = ?, updated_at = ? WHERE id = ? AND status = 'sent'`,
		j.Step, nullUnknown(j.FileSize), j.Response, nullUnknown(j.PoRStatus), nullUnknown(j.StatusWord), j.Warning, j.Data,
		d.now().UnixMilli(), j.ID)
	if err != nil {
		return fmt.Errorf("advance job: %w", err)
	}

	return affected(res)
}

func (d *DB) CountAttempt(ctx context.Context, id int64) (_ int, err error) {
	ctx, span := d.start(ctx, opCountAttempt)

	defer func() { end(span, err) }()

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

func (d *DB) FinishJob(ctx context.Context, j Job) (err error) {
	ctx, span := d.start(ctx, opFinishJob)

	defer func() { end(span, err) }()

	return d.finish(ctx, j, "status IN ('queued', 'sent')")
}

func (d *DB) ReviseJob(ctx context.Context, j Job) (err error) {
	ctx, span := d.start(ctx, opFinishJob)

	defer func() { end(span, err) }()

	return d.finish(ctx, j, "por_missing = 1 AND counter = ?", int64(j.Counter))
}

func (d *DB) finish(ctx context.Context, j Job, where string, args ...any) error {
	if j.Status != JobSucceeded && j.Status != JobUnconfirmed && j.Status != JobFailed {
		return fmt.Errorf("finish job: status %q is not final", j.Status)
	}

	return d.tx(ctx, func(tx *sql.Tx) error {
		now := d.now().UnixMilli()

		res, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, file_size = ?, response = ?, por_status = ?,
			status_word = ?, warning = ?, data = ?, error = ?, por_missing = ?, updated_at = ? WHERE id = ? AND `+where,
			append([]any{
				j.Status, nullUnknown(j.FileSize), j.Response, nullUnknown(j.PoRStatus), nullUnknown(j.StatusWord), j.Warning,
				j.Data, j.Error, j.PoRMissing, now, j.ID,
			}, args...)...)
		if err != nil {
			return fmt.Errorf("finish job: %w", err)
		}

		if err := affected(res); err != nil {
			return err
		}

		if j.Status != JobSucceeded {
			return nil
		}

		if _, err := tx.ExecContext(ctx, `UPDATE cards SET imsi = w.imsi, updated_at = ? FROM jobs AS w
			WHERE w.id = ? AND w.operation = 'imsi_write' AND cards.iccid = w.iccid
			AND NOT EXISTS (SELECT 1 FROM jobs WHERE iccid = w.iccid AND operation = 'imsi_write' AND id > w.id)`,
			now, j.ID); err != nil {
			return fmt.Errorf("finish job: record the card's IMSI: %w", err)
		}

		return nil
	})
}

func (d *DB) jobs(ctx context.Context, clause string, args ...any) ([]Job, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, iccid, operation, slot, mcc, mnc, access, imsi, previous_imsi, tar,
		kvn, status, step, file_size, counter, message_id, command, response, por_status, status_word, warning, data, error,
		por_missing, traceparent, command_traceparent, created_at, updated_at FROM jobs `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var jobs []Job

	for rows.Next() {
		var (
			j                               Job
			slot, access, fileSize          sql.NullInt64
			counter, porStatus, statusWord  sql.NullInt64
			mcc, mnc, messageID             sql.NullString
			imsi, previousIMSI              sql.NullString
			traceparent, commandTraceParent sql.NullString
			created, lastUpdated            int64
		)

		if err := rows.Scan(&j.ID, &j.ICCID, &j.Operation, &slot, &mcc, &mnc, &access, &imsi, &previousIMSI, &j.TAR, &j.KVN,
			&j.Status, &j.Step, &fileSize, &counter, &messageID, &j.Command, &j.Response, &porStatus, &statusWord, &j.Warning,
			&j.Data, &j.Error, &j.PoRMissing, &traceparent, &commandTraceParent, &created, &lastUpdated); err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}

		j.Slot, j.MCC, j.MNC, j.Access, j.MessageID = int(slot.Int64), mcc.String, mnc.String, int(access.Int64), messageID.String
		j.IMSI, j.PreviousIMSI = imsi.String, previousIMSI.String
		j.TraceParent, j.CommandTraceParent = traceparent.String, commandTraceParent.String
		j.Counter = uint64(counter.Int64)
		j.FileSize, j.PoRStatus, j.StatusWord = unknown(fileSize), unknown(porStatus), unknown(statusWord)
		j.CreatedAt, j.UpdatedAt = time.UnixMilli(created), time.UnixMilli(lastUpdated)

		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

func unknown(n sql.NullInt64) int {
	if !n.Valid {
		return -1
	}

	return int(n.Int64)
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}

	return n
}

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

func nullAccess(j Job) any {
	if j.Operation != OperationOPLMNSet {
		return nil
	}

	return j.Access
}
