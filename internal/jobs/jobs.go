package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/ota"
	"github.com/ellanetworks/simota/internal/rfm"
	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/trace"
)

// TS 23.040 clause 9.2.2.1a
const ReadChunk = 125

const MaxSlot = 51

const entryLength = 5

// SMPP 3.4 section 4.4.1 table 4-10
const (
	// SMPP 3.4 section 5.2.5
	// SMPP 3.4 section 5.2.6
	tonInternational = 0x01
	npiE164          = 0x01
	// SMPP 3.4 section 5.2.12
	ESMClassUDHI = 0x40
	// SMPP 3.4 section 5.2.13
	// TS 23.040 clause 9.2.3.9
	ProtocolUSIMDataDownload = 0x7f
	// SMPP 3.4 section 5.2.19
	// TS 23.038 clause 4
	// TS 23.040 clause 9.2.3.9
	DCSClass2 = 0xf6
	// SMPP 3.4 section 5.2.17
	RegisteredDelivery = 0x09
)

// SMPP 3.4 section 5.2.12
const (
	messageTypeDefault = 0x0
	messageTypeReceipt = 0x1
	messageTypeUserAck = 0x4
)

// TS 31.115 clause 4.4
var responseUDH = []byte{0x02, 0x71, 0x00}

const answerGrace = 6 * time.Hour

// SMPP 3.4 section 7.1.1.2
const MaxValidity = 99 * 24 * time.Hour

const (
	minBackoff  = 10 * time.Second
	maxBackoff  = 5 * time.Minute
	maxAttempts = 10
)

type Store interface {
	GetCard(ctx context.Context, iccid string) (db.Card, error)
	NextCounter(ctx context.Context, iccid string, version int) (uint64, error)
	JobsIn(ctx context.Context, status db.JobStatus, limit int) ([]db.Job, error)
	SentJob(ctx context.Context, messageID string) (db.Job, error)
	AwaitingPoR(ctx context.Context, msisdn string, tar []byte) ([]db.Job, error)
	MarkJobSent(ctx context.Context, id int64, counter uint64, messageID string, command []byte) error
	CountAttempt(ctx context.Context, id int64) (int, error)
	AdvanceJob(ctx context.Context, j db.Job) error
	FinishJob(ctx context.Context, j db.Job) error
	ReviseJob(ctx context.Context, j db.Job) error
}

type SMSC interface {
	Bound() bool
	SubmitSM(ctx context.Context, m *smpp.SubmitSM) (string, error)
}

const batch = 100

const jobTimeout = time.Minute

type Gateway struct {
	SMSC       SMSC
	Originator string
	Validity   time.Duration
}

var ErrNoGateway = errors.New("no SMSC set")

type Runner struct {
	Store          Store
	Connect        func(ctx context.Context) (Gateway, error)
	Interval       time.Duration
	Logger         *slog.Logger
	Now            func() time.Time
	TracerProvider trace.TracerProvider

	wake chan struct{}
	mu   sync.Mutex

	boundSince time.Time
	holdUntil  time.Time
	backoff    time.Duration
}

func NewRunner(store Store, connect func(ctx context.Context) (Gateway, error), interval time.Duration, logger *slog.Logger) *Runner {
	return &Runner{Store: store, Connect: connect, Interval: interval, Logger: logger, wake: make(chan struct{}, 1)}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}

	return time.Now()
}

func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.Interval)
	defer t.Stop()

	for {
		r.Step(ctx)

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

func (r *Runner) Step(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	gw, err := r.Connect(ctx)
	if errors.Is(err, ErrNoGateway) || stopped(ctx, err) {
		return
	}

	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to get the SMSC", slog.Any("error", err))
		return
	}

	if !gw.SMSC.Bound() {
		r.boundSince = time.Time{}
		r.Logger.DebugContext(ctx, "not bound to the SMSC, jobs wait")

		return
	}

	if r.boundSince.IsZero() {
		r.boundSince = r.now()
	}

	r.sweep(ctx, gw.Validity)

	if r.now().Before(r.holdUntil) {
		return
	}

	queued, err := r.Store.JobsIn(ctx, db.JobQueued, batch)
	if stopped(ctx, err) {
		return
	}

	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to list queued jobs", slog.Any("error", err))
		return
	}

	for _, j := range queued {
		if ctx.Err() != nil || !r.send(ctx, gw, j) {
			return
		}
	}
}

func work(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), jobTimeout)
}

func stopped(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err())
}

func (r *Runner) sweep(ctx context.Context, validity time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if now.Sub(r.boundSince) <= answerGrace {
		return
	}

	sent, err := r.Store.JobsIn(ctx, db.JobSent, batch)
	if stopped(ctx, err) {
		return
	}

	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to list sent jobs", slog.Any("error", err))
		return
	}

	for _, j := range sent {
		if ctx.Err() != nil {
			return
		}

		if now.Sub(j.UpdatedAt) <= validity+answerGrace {
			continue
		}

		next := noPoR(j, "no PoR and no delivery receipt from the SMSC within the validity of the message")
		next.PoRMissing = true

		jctx, cancel := work(ctx)
		jctx, span := r.startProcess(jctx, j)

		setOutcome(jctx, outcomeExpired, next.Error)

		if err := r.finish(jctx, next); err != nil {
			r.Logger.ErrorContext(jctx, "failed to finish job", slog.Int64("job", j.ID), slog.Any("error", err))
		}

		span.End()
		cancel()
	}
}

func (r *Runner) send(ctx context.Context, gw Gateway, j db.Job) bool {
	ctx, cancel := work(ctx)
	defer cancel()

	ctx, span := r.startProcess(ctx, j)
	defer span.End()

	card, err := r.Store.GetCard(ctx, j.ICCID)
	if err != nil {
		return r.storeFailed(ctx, j, fmt.Sprintf("card %s", j.ICCID), err)
	}

	keyset, err := keysetFor(card, j)
	if err != nil {
		r.fail(ctx, j, err.Error())
		return true
	}

	script, err := Script(j)
	if err != nil {
		r.fail(ctx, j, err.Error())
		return true
	}

	data, err := script.Bytes()
	if err != nil {
		r.fail(ctx, j, err.Error())
		return true
	}

	counter, err := r.Store.NextCounter(ctx, j.ICCID, j.KVN)
	if err != nil {
		return r.storeFailed(ctx, j, fmt.Sprintf("key set %d", j.KVN), err)
	}

	cmd := ota.Command{Keyset: keyset, Counter: counter, Data: data}
	copy(cmd.TAR[:], j.TAR)

	udh, ud, err := cmd.Encode()
	if err != nil {
		r.fail(ctx, j, err.Error())
		return true
	}

	sm, err := SubmitSM(gw.Originator, card.MSISDN, gw.Validity, append(udh, ud...))
	if err != nil {
		r.fail(ctx, j, err.Error())
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	id, err := gw.SMSC.SubmitSM(ctx, sm)

	switch {
	case errors.Is(err, smpp.ErrNoSession):
		r.Logger.DebugContext(ctx, "not bound to the SMSC, jobs wait", slog.Int64("job", j.ID))
		setOutcome(ctx, outcomeWaiting, "")

		return false
	case Rejected(err):
		r.fail(ctx, j, fmt.Sprintf("SMSC refused the message: %v", err))

		return true
	case err != nil:
		return r.retry(ctx, j, err)
	}

	r.backoff = 0

	if err := r.Store.MarkJobSent(ctx, j.ID, counter, id, sm.ShortMessage); err != nil {
		r.Logger.ErrorContext(ctx, "failed to mark job sent", slog.Int64("job", j.ID), slog.String("message", id), slog.Any("error", err))
		return r.retry(ctx, j, fmt.Errorf("record message %s: %w", id, err))
	}

	setOutcome(ctx, outcomeSent, "")

	return true
}

func (r *Runner) storeFailed(ctx context.Context, j db.Job, what string, err error) bool {
	if errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrCounterExhausted) {
		r.fail(ctx, j, fmt.Sprintf("%s: %v", what, err))
		return true
	}

	r.Logger.ErrorContext(ctx, "failed to read the store for a job, it waits", slog.Int64("job", j.ID), slog.String("what", what),
		slog.Any("error", err))
	setOutcome(ctx, outcomeWaiting, "")

	return false
}

// SMPP 3.4 section 5.1.3
func (r *Runner) retry(ctx context.Context, j db.Job, err error) bool {
	n, cerr := r.Store.CountAttempt(ctx, j.ID)
	if cerr != nil {
		r.Logger.ErrorContext(ctx, "failed to count a submit attempt", slog.Int64("job", j.ID), slog.Any("error", cerr))
	}

	if n >= maxAttempts {
		r.fail(ctx, j, fmt.Sprintf("the SMSC did not take the message in %d attempts: %v", maxAttempts, err))
		return true
	}

	r.Logger.WarnContext(ctx, "failed to submit job, will retry", slog.Int64("job", j.ID), slog.Int("attempt", n), slog.Any("error", err))
	setOutcome(ctx, outcomeRetry, "")

	var se *smpp.StatusError
	if !errors.As(err, &se) {
		return true
	}

	switch se.Status {
	case smpp.StatusThrottled, smpp.StatusMsgQFul:
		r.backoff = min(max(2*r.backoff, minBackoff), maxBackoff)
		r.holdUntil = r.now().Add(r.backoff)

		return false
	case smpp.StatusInvBndSts:
		return false
	default:
		return true
	}
}

// SMPP 3.4 section 4.4.1
func SubmitSM(originator, msisdn string, validity time.Duration, packet []byte) (*smpp.SubmitSM, error) {
	vp, err := ValidityPeriod(validity)
	if err != nil {
		return nil, err
	}

	return &smpp.SubmitSM{Message: smpp.Message{
		Source:             smpp.Address{TON: tonInternational, NPI: npiE164, Addr: strings.TrimPrefix(originator, "+")},
		Destination:        smpp.Address{TON: tonInternational, NPI: npiE164, Addr: strings.TrimPrefix(msisdn, "+")},
		ESMClass:           ESMClassUDHI,
		ProtocolID:         ProtocolUSIMDataDownload,
		ValidityPeriod:     vp,
		RegisteredDelivery: RegisteredDelivery,
		DataCoding:         DCSClass2,
		ShortMessage:       packet,
	}}, nil
}

// SMPP 3.4 section 7.1.1.2
func ValidityPeriod(validity time.Duration) (string, error) {
	rest := validity.Round(time.Second)
	if rest < time.Second || rest > MaxValidity {
		return "", fmt.Errorf("validity %v is not between 1 second and %v", validity, MaxValidity)
	}

	var p smpp.RelativePeriod

	for _, f := range []struct {
		n    *int
		unit time.Duration
	}{{&p.Days, 24 * time.Hour}, {&p.Hours, time.Hour}, {&p.Minutes, time.Minute}, {&p.Seconds, time.Second}} {
		*f.n = int(rest / f.unit)
		rest -= time.Duration(*f.n) * f.unit
	}

	return smpp.FormatRelative(p)
}

// SMPP 3.4 section 5.1.3 table 5-2
func Rejected(err error) bool {
	var se *smpp.StatusError
	if !errors.As(err, &se) {
		return false
	}

	switch se.Status {
	case smpp.StatusThrottled, smpp.StatusMsgQFul, smpp.StatusSysErr, smpp.StatusInvBndSts:
		return false
	default:
		return true
	}
}

func keysetFor(card db.Card, j db.Job) (ota.Keyset, error) {
	if card.MSISDN == "" {
		return ota.Keyset{}, errors.New("the card has no MSISDN")
	}

	keyset, ok := keysetOf(card, j.KVN)
	if !ok {
		return ota.Keyset{}, fmt.Errorf("the card has no key set %d", j.KVN)
	}

	return keyset, nil
}

func keysetOf(card db.Card, kvn int) (ota.Keyset, bool) {
	for _, k := range card.KeySets {
		if k.Version != kvn {
			continue
		}

		keyset := ota.Keyset{Version: uint8(k.Version), Algorithm: ota.TripleDES, KIc: k.KIc, KID: k.KID, CCLength: k.CCLength}
		if k.Algorithm == "aes" {
			keyset.Algorithm = ota.AES
		}

		return keyset, true
	}

	return ota.Keyset{}, false
}

// SMPP 3.4 section 2.11
// SMPP 3.4 section 5.2.12
func (r *Runner) ServeSMPP(ctx context.Context, s *smpp.Session, req smpp.Request) smpp.Response {
	d, ok := req.PDU.(*smpp.DeliverSM)
	if !ok {
		return smpp.Response{}
	}

	ctx, span := r.startDeliver(ctx, s)
	answered := false

	defer func() {
		if !answered {
			tracing.EndSMPP(span, new(smpp.StatusSysErr), nil)
		}
	}()

	status, cause := smpp.StatusOK, r.Deliver(ctx, d)
	if cause != nil {
		r.Logger.ErrorContext(ctx, "failed to record a deliver_sm, the SMSC will send it again", slog.Any("error", cause))

		status = smpp.StatusXTAppn
	}

	answered = true

	return smpp.Response{Status: status, Written: func(err error) {
		if err == nil {
			err = cause
		}

		tracing.EndSMPP(span, &status, err)
	}}
}

func (r *Runner) Deliver(ctx context.Context, d *smpp.DeliverSM) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch d.ESMClass >> 2 & 0x0f {
	case messageTypeReceipt:
		deliverKind(ctx, deliverReceipt)
		return r.receipt(ctx, d)
	case messageTypeDefault, messageTypeUserAck:
		if packet, ok := responsePacket(d); ok {
			deliverKind(ctx, deliverPoR)
			return r.answer(ctx, d, packet)
		}
	}

	deliverKind(ctx, deliverOther)
	deliverOutcome(ctx, deliverIgnored, "")

	r.Logger.InfoContext(ctx, "ignoring a deliver_sm that is neither a PoR nor a receipt", slog.Int("esm_class", int(d.ESMClass)),
		slog.String("source", d.Source.Addr))

	return nil
}

// SMPP 3.4 section 4.6.1
// TS 31.115 clause 4.1
func responsePacket(d *smpp.DeliverSM) ([]byte, bool) {
	sm := d.ShortMessage
	if len(sm) == 0 {
		sm = d.Options.MessagePayload
	}

	return sm, bytes.HasPrefix(sm, responseUDH) && len(sm) >= len(responseUDH)+6
}

// ETSI TS 102 225 clause 5.2 table 3
func (r *Runner) answer(ctx context.Context, d *smpp.DeliverSM, packet []byte) error {
	udh, ud := packet[:len(responseUDH)], packet[len(responseUDH):]
	tar := ud[3:6]

	candidates, err := r.Store.AwaitingPoR(ctx, "+"+strings.TrimPrefix(d.Source.Addr, "+"), tar)
	if err != nil {
		return err
	}

	inFlight := 0

	for _, j := range candidates {
		if j.Status == db.JobSent {
			inFlight++
		}
	}

	var decodeErr error

	for _, j := range candidates {
		card, err := r.Store.GetCard(ctx, j.ICCID)
		if errors.Is(err, db.ErrNotFound) {
			continue
		}

		if err != nil {
			return fmt.Errorf("card %s: %w", j.ICCID, err)
		}

		keyset, ok := keysetOf(card, j.KVN)
		if !ok {
			if j.Status == db.JobSent && inFlight == 1 {
				next := noPoR(j, fmt.Sprintf("the card has no key set %d to read its PoR", j.KVN))

				deliverMatched(ctx, j)

				return recorded(ctx, deliverRecorded, next.Status, r.finish(ctx, next))
			}

			continue
		}

		resp, err := ota.DecodeResponse(keyset, udh, ud)
		if err != nil {
			decodeErr = err
			continue
		}

		if resp.Unsecured && (j.Status != db.JobSent || inFlight != 1) || !resp.Unsecured && resp.Counter != j.Counter {
			continue
		}

		if id := d.Options.ReceiptedMessageID; id != nil && *id != j.MessageID {
			r.Logger.DebugContext(ctx, "a PoR's receipted_message_id is not its command's message_id", slog.Int64("job", j.ID),
				slog.String("receipted_message_id", *id), slog.String("message_id", j.MessageID))
		}

		deliverMatched(ctx, j)

		return r.record(ctx, j, resp, packet)
	}

	deliverOutcome(ctx, deliverUnmatched, "")
	r.Logger.InfoContext(ctx, "ignoring a PoR that answers no job's command", slog.String("source", d.Source.Addr),
		slog.String("tar", fmt.Sprintf("%x", tar)), slog.Any("error", decodeErr))

	return nil
}

func (r *Runner) record(ctx context.Context, j db.Job, resp ota.Response, packet []byte) error {
	next, final := Outcome(j, resp, packet)

	if j.Status == db.JobSent {
		if final {
			return recorded(ctx, deliverRecorded, next.Status, r.finish(ctx, next))
		}

		if err := r.Store.AdvanceJob(ctx, next); err != nil && !errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("advance job %d: %w", j.ID, err)
		}

		deliverOutcome(ctx, deliverRecorded, db.JobQueued)
		r.Wake()

		return nil
	}

	if !final {
		deliverOutcome(ctx, deliverIgnored, "")
		r.Logger.InfoContext(ctx, "ignoring a late PoR to a step of a read closed without it", slog.Int64("job", j.ID))

		return nil
	}

	r.Logger.InfoContext(ctx, "a late PoR answers a job closed without it", slog.Int64("job", j.ID), slog.String("was", string(j.Status)))

	next.PoRMissing = false

	if err := r.Store.ReviseJob(ctx, next); err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("revise job %d: %w", j.ID, err)
	}

	deliverOutcome(ctx, deliverRevised, next.Status)
	r.logFinished(ctx, next)

	return nil
}

// SMPP 3.4 section 2.11
// SMPP 3.4 section 5.2.17
// SMPP 3.4 section 4.6.1 table 4-18
// SMPP 3.4 appendix B
func (r *Runner) receipt(ctx context.Context, d *smpp.DeliverSM) error {
	id := receiptField(d.ShortMessage, "id")
	if d.Options.ReceiptedMessageID != nil {
		id = *d.Options.ReceiptedMessageID
	}

	if id == "" {
		deliverOutcome(ctx, deliverUnmatched, "")
		r.Logger.InfoContext(ctx, "ignoring a receipt without a message id", slog.String("text", string(d.ShortMessage)))

		return nil
	}

	j, err := r.Store.SentJob(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		deliverOutcome(ctx, deliverUnmatched, "")
		r.Logger.DebugContext(ctx, "ignoring a receipt for no job in progress", slog.String("message", id))

		return nil
	}

	if err != nil {
		return err
	}

	next := noPoR(j, receiptReason(d))
	next.PoRMissing = true

	deliverMatched(ctx, j)

	return recorded(ctx, deliverRecorded, next.Status, r.finish(ctx, next))
}

func recorded(ctx context.Context, outcome string, status db.JobStatus, err error) error {
	if err == nil {
		deliverOutcome(ctx, outcome, status)
	}

	return err
}

func receiptReason(d *smpp.DeliverSM) string {
	state, ok := receiptState(d)
	if !ok {
		return "message in an unknown state, without a PoR"
	}

	switch state {
	case smpp.StateDelivered:
		return "delivered without a PoR: the card answers none to a sender it cannot authenticate, such as with other " +
			"keys or key set; or the serving node passed none on"
	case smpp.StateExpired:
		return "message expired without a PoR"
	case smpp.StateUndeliverable:
		return "message undeliverable without a PoR"
	case smpp.StateDeleted:
		return "message deleted by the SMSC without a PoR"
	case smpp.StateRejected:
		return "message rejected by the SMSC without a PoR"
	case smpp.StateAccepted:
		return "message accepted on the subscriber's behalf, without a PoR"
	default:
		return fmt.Sprintf("message in state %s, without a PoR", state)
	}
}

// SMPP 3.4 section 5.3.2.35
// SMPP 3.4 appendix B table B-2
func receiptState(d *smpp.DeliverSM) (smpp.MessageState, bool) {
	if s := d.Options.MessageState; s != nil {
		return *s, true
	}

	states := map[string]smpp.MessageState{
		"DELIVRD": smpp.StateDelivered, "EXPIRED": smpp.StateExpired, "DELETED": smpp.StateDeleted,
		"UNDELIV": smpp.StateUndeliverable, "ACCEPTD": smpp.StateAccepted, "UNKNOWN": smpp.StateUnknown,
		"REJECTD": smpp.StateRejected,
	}

	s, ok := states[receiptField(d.ShortMessage, "stat")]

	return s, ok
}

// SMPP 3.4 appendix B
func receiptField(text []byte, name string) string {
	for f := range strings.FieldsSeq(string(text)) {
		if v, ok := strings.CutPrefix(f, name+":"); ok {
			return v
		}

		if strings.HasPrefix(strings.ToLower(f), "text:") {
			break
		}
	}

	return ""
}

func noPoR(j db.Job, reason string) db.Job {
	j.Status, j.Error = db.JobFailed, asSentence(reason)
	if writes(j) {
		j.Status = db.JobUnconfirmed
	}

	return j
}

func writes(j db.Job) bool {
	return j.Operation == db.OperationOPLMNSet || j.Operation == db.OperationOPLMNClear || j.Operation == db.OperationIMSIWrite
}

func Outcome(j db.Job, r ota.Response, packet []byte) (db.Job, bool) {
	j.Response = append([]byte{}, packet...)
	j.Warning, j.Error = false, ""

	fail := func(status db.JobStatus, format string, a ...any) (db.Job, bool) {
		j.Status, j.Error = status, fmt.Sprintf(format, a...)
		return j, true
	}

	j.PoRStatus, j.StatusWord = int(r.Status), -1

	switch r.Status {
	case ota.StatusPoROK:
	case ota.StatusResponseInSubmit, ota.StatusMoreTime:
		status := db.JobFailed
		if writes(j) {
			status = db.JobUnconfirmed
		}

		return fail(status, "The card sends its result later, by SMS-SUBMIT: %s.", r.Status.Label())
	case ota.StatusTARUnknown:
		return fail(db.JobFailed, "The card has no application at TAR %x: set the card's ADF RFM TAR.", j.TAR)
	default:
		if r.Unsecured {
			return fail(db.JobFailed, "The card refused the command, in an unsecured PoR: %s.", r.Status.Label())
		}

		return fail(db.JobFailed, "The card refused the command: %s.", r.Status.Label())
	}

	rr, err := rfm.DecodeResponse(r.Data)
	if err != nil {
		return fail(db.JobFailed, "%s", sentence(err))
	}

	j.StatusWord = int(rr.StatusWord)

	script, err := Script(j)
	if err != nil {
		return fail(db.JobFailed, "%s", sentence(err))
	}

	res, err := script.Check(rr)

	var se *rfm.ScriptError
	if errors.As(err, &se) && writes(j) && se.Executed == script.Len() {
		return fail(db.JobUnconfirmed, "Written, but the read-back failed: %v.", err)
	}

	if err != nil {
		return fail(db.JobFailed, "%s", sentence(err))
	}

	j.Warning = res.Warning

	return advance(j, rr.Data)
}

func advance(j db.Job, data []byte) (db.Job, bool) {
	fail := func(format string, a ...any) (db.Job, bool) {
		j.Status, j.Error = db.JobFailed, fmt.Sprintf(format, a...)
		return j, true
	}

	if writes(j) {
		w, err := writeOf(j)
		if err != nil {
			return fail("%s", sentence(err))
		}

		j.Data = data
		if !bytes.Equal(data, w.data) {
			return fail("%s reads back %s instead of %s.", w.what, w.contents(data), w.contents(w.data))
		}

		j.Status = db.JobSucceeded

		return j, true
	}

	if j.Step == 0 {
		size, err := rfm.FileSize(data)
		if err != nil {
			return fail("%s", sentence(err))
		}

		if size%entryLength != 0 {
			return fail("EF.OPLMNwAcT of %d octets is not a list of %d-octet entries.", size, entryLength)
		}

		j.FileSize, j.Data = size, nil
	} else {
		if want := chunkLength(j); len(data) != want {
			return fail("Read %d octets, not %d.", len(data), want)
		}

		j.Data = append(j.Data, data...)
	}

	if len(j.Data) == j.FileSize {
		j.Status = db.JobSucceeded
		return j, true
	}

	j.Step++

	return j, false
}

// ETSI TS 102 226 clause 7.3
func Script(j db.Job) (*rfm.Script, error) {
	s := rfm.NewScript()

	switch j.Operation {
	case db.OperationOPLMNRead:
		if j.Step == 0 {
			return s.SelectFCP(rfm.FileOPLMNwAcT).GetResponse(), nil
		}

		n := chunkLength(j)
		if j.FileSize < 0 || n <= 0 {
			return nil, fmt.Errorf("no step %d in a file of %d octets", j.Step, j.FileSize)
		}

		return s.Select(rfm.FileOPLMNwAcT).ReadBinary(uint16((j.Step-1)*ReadChunk), uint8(n)), nil
	case db.OperationOPLMNSet, db.OperationOPLMNClear, db.OperationIMSIWrite:
		w, err := writeOf(j)
		if err != nil {
			return nil, err
		}

		return s.Select(w.file).UpdateBinary(w.offset, w.data).ReadBinary(w.offset, uint8(len(w.data))), nil
	default:
		return nil, fmt.Errorf("unknown operation %q", j.Operation)
	}
}

func chunkLength(j db.Job) int {
	return min(ReadChunk, j.FileSize-(j.Step-1)*ReadChunk)
}

type write struct {
	file   uint16
	offset uint16
	data   []byte
	what   string
}

func writeOf(j db.Job) (write, error) {
	if j.Operation == db.OperationIMSIWrite {
		ef, err := rfm.EncodeIMSI(j.IMSI)
		if err != nil {
			return write{}, err
		}

		return write{file: rfm.FileIMSI, data: ef, what: "EF.IMSI"}, nil
	}

	if j.Slot < 1 || j.Slot > MaxSlot {
		return write{}, fmt.Errorf("slot %d not in 1-%d", j.Slot, MaxSlot)
	}

	e := rfm.Empty
	if j.Operation == db.OperationOPLMNSet {
		e = rfm.SelectorEntry{MCC: j.MCC, MNC: j.MNC, Access: rfm.AccessTechnology(j.Access)}
	}

	entry, err := e.Encode()
	if err != nil {
		return write{}, err
	}

	return write{file: rfm.FileOPLMNwAcT, offset: rfm.SlotOffset(j.Slot), data: entry, what: fmt.Sprintf("Slot %d", j.Slot)}, nil
}

func (w write) contents(b []byte) string {
	if w.file == rfm.FileIMSI {
		if imsi, err := rfm.DecodeIMSI(b); err == nil {
			return imsi
		}

		return fmt.Sprintf("%x", b)
	}

	es, err := rfm.DecodeSelector(b)
	if err != nil || len(es) != 1 {
		return fmt.Sprintf("%x", b)
	}

	if es[0] == rfm.Empty {
		return "an empty entry"
	}

	var labels []string
	for _, n := range es[0].Access.Names() {
		labels = append(labels, rfm.AccessLabels[n])
	}

	access := strings.Join(labels, ", ")
	if access == "" {
		access = "none"
	}

	return fmt.Sprintf("%s/%s (%s)", es[0].MCC, es[0].MNC, access)
}

func sentence(err error) string {
	return asSentence(err.Error())
}

func asSentence(s string) string {
	if s == "" {
		return s
	}

	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}

	return s
}

func (r *Runner) fail(ctx context.Context, j db.Job, reason string) {
	j.Status, j.Error = db.JobFailed, asSentence(reason)
	setOutcome(ctx, outcomeFailed, j.Error)

	if err := r.finish(ctx, j); err != nil {
		r.Logger.ErrorContext(ctx, "failed to finish job", slog.Int64("job", j.ID), slog.Any("error", err))
	}
}

func (r *Runner) finish(ctx context.Context, j db.Job) error {
	err := r.Store.FinishJob(ctx, j)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("finish job %d: %w", j.ID, err)
	}

	r.logFinished(ctx, j)

	return nil
}

func (r *Runner) logFinished(ctx context.Context, j db.Job) {
	r.Logger.InfoContext(ctx, "job finished", slog.Int64("job", j.ID), slog.String("iccid", j.ICCID), slog.String("status", string(j.Status)),
		slog.String("error", j.Error))
}
