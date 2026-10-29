// Package jobs runs OTA jobs: it sends each queued job's commands to its card through an SMSC, such as Ella SMSC, over
// SMPP 3.4, one at a time, and records the card's answers and the SMSC's delivery receipts that the SMSC pushes back
// as deliver_sm.
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
	"github.com/ellanetworks/smsc/smpp"
)

// ReadChunk is the most file octets a read command asks for: 25 entries. The PoR, with its header, a CC of 8 octets,
// the compact response header and the data padded to the cipher block, then fits the 159 octets of the
// SMS-DELIVER-REPORT's TP-UD (TS 23.040 clause 9.2.2.1a); a larger one would come by SMS-SUBMIT (TS 31.115 clause 4.1).
const ReadChunk = 125

// MaxSlot is the last slot a job may write: EF.OPLMNwAcT fits in 255 octets, the largest size an FCP of one octet
// tells.
const MaxSlot = 51

// entryLength is the length of an EF.OPLMNwAcT entry.
const entryLength = 5

// The submit_sm of a command (SMPP 3.4 section 4.4.1, table 4-10).
const (
	// TON and NPI of both addresses: international, E.164 (SMPP 3.4 sections 5.2.5 and 5.2.6).
	tonInternational = 0x01
	npiE164          = 0x01
	// ESMClassUDHI is esm_class with the UDHI indicator, bits 7-6 = 01, and the default mode and message type (SMPP
	// 3.4 section 5.2.12): short_message starts with the command packet's user data header.
	ESMClassUDHI = 0x40
	// ProtocolUSIMDataDownload is the protocol_id, the TP-PID of (U)SIM data download (SMPP 3.4 section 5.2.13: "set
	// according to GSM 03.40"; TS 23.040 clause 9.2.3.9).
	ProtocolUSIMDataDownload = 0x7f
	// DCSClass2 is the data_coding 1111 0110, GSM message class control (SMPP 3.4 section 5.2.19, note e): 8-bit data
	// of message class 2, (U)SIM specific (TS 23.038 clause 4), which TS 23.040 clause 9.2.3.9 requires for TP-PID 7F.
	DCSClass2 = 0xf6
	// RegisteredDelivery asks for an SME Manual/User Acknowledgement, bits 3-2 = 10, which may carry the card's PoR,
	// and for an SMSC delivery receipt on success or failure, bits 1-0 = 01 (SMPP 3.4 section 5.2.17). An SMSC that
	// does not support the acknowledgement ignores it; the PoR is recognised by its content however it comes.
	RegisteredDelivery = 0x09
)

// The message types of a deliver_sm's esm_class, bits 5-2 (SMPP 3.4 section 5.2.12). The card's answer comes as a
// normal message or as an SME Manual/User Acknowledgement, as the SMSC chooses; a receipt is an SMSC Delivery Receipt.
const (
	messageTypeDefault = 0x0
	messageTypeReceipt = 0x1
	messageTypeUserAck = 0x4
)

// responseUDH is the user data header of a Response Packet in a single short message: its length, then IEI '71' with
// a null length (TS 31.115 clause 4.4).
var responseUDH = []byte{0x02, 0x71, 0x00}

// answerGrace is how long past the validity a sent job waits, while bound, for the card's answer or the SMSC's
// receipt before it is closed without a PoR. The SMSC sends the receipt when it gives up on the message, at the end of
// the validity, and sends a deliver_sm again when simota answers it with a temporary error or is unbound. SMPP 3.4
// leaves the SMSC's retry schedule open; Ella SMSC doubles its interval up to 1 hour, so 6 hours leave room for
// several retries at the longest interval of such a schedule. A PoR that comes later still gives the job its outcome
// (see record), so the grace only bounds how long a job shows as sent, and the validity is the one set now.
const answerGrace = 6 * time.Hour

// MaxValidity is the longest validity of a command: 99 days, the most that a relative validity_period with
// normalised fields tells (SMPP 3.4 section 7.1.1.2). An OTA command older than that is stale anyway.
const MaxValidity = 99 * 24 * time.Hour

// After the SMSC throttles simota or has its queue full, the runner sends nothing for a backoff that doubles from
// minBackoff to maxBackoff. A command that the SMSC fails to take maxAttempts times fails: without a bound, an SMSC
// that keeps answering ESME_RSYSERR or timing out would hold the card's job queued forever, and the backoff alone only
// slows that down. The count is on the job, so a restart does not reset it.
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

// SMSC is the SMPP session to the SMSC.
type SMSC interface {
	// Bound reports whether the session is bound: jobs wait while it is not.
	Bound() bool
	// SubmitSM returns the message_id, smpp.ErrNoSession while not bound, or a *smpp.StatusError when the SMSC
	// refuses the message.
	SubmitSM(ctx context.Context, m *smpp.SubmitSM) (string, error)
}

// batch is the most jobs of a status that a step handles.
const batch = 100

// Gateway is the SMSC that OTA messages go through, the address they come from, and how long the SMSC attempts their
// delivery.
type Gateway struct {
	SMSC       SMSC
	Originator string
	Validity   time.Duration
}

// ErrNoGateway is a Connect that finds no SMSC set.
var ErrNoGateway = errors.New("no SMSC set")

// Runner sends the queued jobs' commands at each step, and takes the SMSC's deliver_sm as a smpp.Handler.
type Runner struct {
	Store Store
	// Connect gives the gateway as currently set, or ErrNoGateway. Jobs wait while there is none.
	Connect func(ctx context.Context) (Gateway, error)
	// Interval is the time between steps.
	Interval time.Duration
	Logger   *slog.Logger
	// Now is the clock, time.Now when nil.
	Now func() time.Time

	wake chan struct{}
	// mu orders a submit_sm and the recording of its counter and message_id before any deliver_sm about it is
	// handled, and serialises the changes of sent jobs. A deliver_sm waits for the submit_sm in progress, at most the
	// session's response timer, which is acceptable at simota's rate.
	mu sync.Mutex

	// The fields below are Step's.
	//
	// boundSince is when Step first saw the session bound, zero while unbound. holdUntil ends the backoff after a
	// throttling, of length backoff.
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

// Wake makes Run step now, for jobs just queued.
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run steps every Interval, and when woken, until ctx ends.
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

// Step closes the sent jobs that nothing answered in time, then sends the queued ones, all only while bound: the SMSC
// holds its deliver_sm while simota is unbound. A job whose submit_sm fails without the SMSC refusing it stays
// queued, for a later step.
func (r *Runner) Step(ctx context.Context) {
	gw, err := r.Connect(ctx)
	if errors.Is(err, ErrNoGateway) {
		return
	}

	if err != nil {
		r.Logger.Error("failed to get the SMSC", slog.Any("error", err))
		return
	}

	if !gw.SMSC.Bound() {
		r.boundSince = time.Time{}
		r.Logger.Debug("not bound to the SMSC, jobs wait")

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
	if err != nil {
		r.Logger.Error("failed to list queued jobs", slog.Any("error", err))
		return
	}

	for _, j := range queued {
		if !r.send(ctx, gw, j) {
			return
		}
	}
}

// sweep closes, without a PoR, the sent jobs answered by nothing answerGrace past the validity, nor in answerGrace of
// being bound, which leaves the SMSC time to send what it held while simota was unbound. It holds r.mu, so that no
// deliver_sm changes a job between its listing and its closing.
func (r *Runner) sweep(ctx context.Context, validity time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if now.Sub(r.boundSince) <= answerGrace {
		return
	}

	sent, err := r.Store.JobsIn(ctx, db.JobSent, batch)
	if err != nil {
		r.Logger.Error("failed to list sent jobs", slog.Any("error", err))
		return
	}

	for _, j := range sent {
		// A sent job's updated_at is when its current command was sent.
		if now.Sub(j.UpdatedAt) <= validity+answerGrace {
			continue
		}

		next := noPoR(j, "no PoR and no delivery receipt from the SMSC within the validity of the message")
		next.PoRMissing = true

		if err := r.finish(ctx, next); err != nil {
			r.Logger.Error("failed to finish job", slog.Int64("job", j.ID), slog.Any("error", err))
		}
	}
}

// send sends a queued job's current command, and reports whether the next job may be sent: not while unbound or
// throttled.
func (r *Runner) send(ctx context.Context, gw Gateway, j db.Job) bool {
	card, keyset, err := r.cardOf(ctx, j)
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
		r.fail(ctx, j, fmt.Sprintf("key set %d: %v", j.KVN, err))
		return true
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
		r.Logger.Debug("not bound to the SMSC, jobs wait", slog.Int64("job", j.ID))
		return false
	case Rejected(err):
		r.fail(ctx, j, fmt.Sprintf("SMSC refused the message: %v", err))

		return true
	case err != nil:
		// The counter is spent: the next attempt takes a higher one, which the card accepts.
		return r.retry(ctx, j, err)
	}

	r.backoff = 0

	// If the message cannot be recorded, the job stays queued and a later step sends its command again, with a new,
	// higher counter. The card executes it as a new command (TS 102 225 clause 5.1.4), which is harmless: a command
	// writes the same octets and reads them back, or reads. The first message's PoR carries the spent counter, so it
	// answers no job's command and is left aside; a duplicate never reuses a counter, which the card would refuse.
	if err := r.Store.MarkJobSent(ctx, j.ID, counter, id, sm.ShortMessage); err != nil {
		r.Logger.Error("failed to mark job sent", slog.Int64("job", j.ID), slog.String("message", id), slog.Any("error", err))
		return r.retry(ctx, j, fmt.Errorf("record message %s: %w", id, err))
	}

	return true
}

// retry counts a failed submit of a job's command and fails the job at the maxAttempts-th. After a throttling or a
// full queue (SMPP 3.4 section 5.1.3), it backs off and stops the batch; while the SMSC unbinds, it stops it too.
func (r *Runner) retry(ctx context.Context, j db.Job, err error) bool {
	n, cerr := r.Store.CountAttempt(ctx, j.ID)
	if cerr != nil {
		r.Logger.Error("failed to count a submit attempt", slog.Int64("job", j.ID), slog.Any("error", cerr))
	}

	if n >= maxAttempts {
		r.fail(ctx, j, fmt.Sprintf("the SMSC did not take the message in %d attempts: %v", maxAttempts, err))
		return true
	}

	r.Logger.Warn("failed to submit job, will retry", slog.Int64("job", j.ID), slog.Int("attempt", n), slog.Any("error", err))

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

// SubmitSM is the submit_sm of a command packet, user data header included, from originator to the card's msisdn,
// both E.164 with a leading "+", valid for validity. It uses only the mandatory parameters of SMPP 3.4 section 4.4.1.
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

// ValidityPeriod is the validity_period of a validity of 1 second to MaxValidity: a relative time (SMPP 3.4 section
// 7.1.1.2) in days, hours (00-23), minutes and seconds (00-59), to the second.
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

// Rejected reports whether err is the SMSC refusing a submit_sm, which sending it again would not change, rather
// than failing to answer it: any command_status but a throttling, a full queue, a system error, or a bind status that
// does not allow it, as while the SMSC unbinds (SMPP 3.4 section 5.1.3, table 5-2).
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

// cardOf is the card of a job, and the job's key set on it.
func (r *Runner) cardOf(ctx context.Context, j db.Job) (db.Card, ota.Keyset, error) {
	card, err := r.Store.GetCard(ctx, j.ICCID)
	if err != nil {
		return db.Card{}, ota.Keyset{}, fmt.Errorf("card %s: %w", j.ICCID, err)
	}

	if card.MSISDN == "" {
		return db.Card{}, ota.Keyset{}, errors.New("the card has no MSISDN")
	}

	keyset, ok := keysetOf(card, j.KVN)
	if !ok {
		return db.Card{}, ota.Keyset{}, fmt.Errorf("the card has no key set %d", j.KVN)
	}

	return card, keyset, nil
}

// keysetOf is the key set of version kvn of a card.
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

// ServeSMPP takes the SMSC's deliver_sm: the card's answer or the SMSC's delivery receipt (SMPP 3.4 sections 2.11 and
// 5.2.12). It answers ESME_RX_T_APPN when it cannot record the outcome, so that the SMSC sends the deliver_sm again
// later, and ESME_ROK otherwise, for a deliver_sm of no job too.
func (r *Runner) ServeSMPP(ctx context.Context, _ *smpp.Session, req smpp.Request) smpp.Response {
	d, ok := req.PDU.(*smpp.DeliverSM)
	if !ok {
		return smpp.Response{}
	}

	if err := r.Deliver(ctx, d); err != nil {
		r.Logger.Error("failed to record a deliver_sm, the SMSC will send it again", slog.Any("error", err))
		return smpp.Response{Status: smpp.StatusXTAppn}
	}

	return smpp.Response{}
}

// Deliver records a deliver_sm on the job it is about. It is an error only when the outcome could not be stored.
func (r *Runner) Deliver(ctx context.Context, d *smpp.DeliverSM) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch d.ESMClass >> 2 & 0x0f {
	case messageTypeReceipt:
		return r.receipt(ctx, d)
	case messageTypeDefault, messageTypeUserAck:
		if packet, ok := responsePacket(d); ok {
			return r.answer(ctx, d, packet)
		}
	}

	r.Logger.Info("ignoring a deliver_sm that is neither a PoR nor a receipt", slog.Int("esm_class", int(d.ESMClass)),
		slog.String("source", d.Source.Addr))

	return nil
}

// responsePacket is the user data of a deliver_sm, in short_message or else in message_payload (SMPP 3.4 section
// 4.6.1), when it is a Response Packet: it starts with the Response Packet's user data header, whether or not UDHI
// is set, since the card cannot have the ME set it (TS 31.115 clause 4.1).
func responsePacket(d *smpp.DeliverSM) ([]byte, bool) {
	sm := d.ShortMessage
	if len(sm) == 0 {
		sm = d.Options.MessagePayload
	}

	// RPL 2, RHL 1 and TAR 3 follow the header.
	return sm, bytes.HasPrefix(sm, responseUDH) && len(sm) >= len(responseUDH)+6
}

// answer records a PoR, from the card of the deliver_sm's source, on the job whose last command it answers: the
// Response Packet's TAR and CNTR are copies of the Command Packet's (TS 102 225 clause 5.2, table 3), so the PoR
// answers the job of the card's number, of its TAR, whose last command had its counter, whether the job is sent or
// was closed without a PoR, by a receipt or the deadline. receipted_message_id, which the SMSC may or may not set, is
// not needed.
//
// An unsecured PoR, which a card that cannot authenticate the command answers, carries a zero counter (TS 102 225
// clause 5.2): it can only be matched by number and TAR, so it is taken for the one job of that number and TAR in
// flight, and left aside when there is none or more than one. A late unsecured PoR is not told apart from one for a
// later command of the same TAR; it is then recorded on that command.
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
				// The PoR of this card's command cannot be read: a configuration error, which a retry does not fix.
				return r.finish(ctx, noPoR(j, fmt.Sprintf("the card has no key set %d to read its PoR", j.KVN)))
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
			r.Logger.Debug("a PoR's receipted_message_id is not its command's message_id", slog.Int64("job", j.ID),
				slog.String("receipted_message_id", *id), slog.String("message_id", j.MessageID))
		}

		return r.record(ctx, j, resp, packet)
	}

	r.Logger.Info("ignoring a PoR that answers no job's command", slog.String("source", d.Source.Addr),
		slog.String("tar", fmt.Sprintf("%x", tar)), slog.Any("error", decodeErr))

	return nil
}

// record records the PoR of a job's last command: on a sent job, its outcome or next command; on a job closed
// without a PoR, the outcome that replaces the one given without it. A read closed without the PoR to one of its steps
// stays failed when the PoR is not its last: its operator runs it again.
func (r *Runner) record(ctx context.Context, j db.Job, resp ota.Response, packet []byte) error {
	next, final := Outcome(j, resp, packet)

	if j.Status == db.JobSent {
		if final {
			return r.finish(ctx, next)
		}

		if err := r.Store.AdvanceJob(ctx, next); err != nil && !errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("advance job %d: %w", j.ID, err)
		}

		r.Wake()

		return nil
	}

	if !final {
		r.Logger.Info("ignoring a late PoR to a step of a read closed without it", slog.Int64("job", j.ID))
		return nil
	}

	r.Logger.Info("a late PoR answers a job closed without it", slog.Int64("job", j.ID), slog.String("was", string(j.Status)))

	next.PoRMissing = false

	if err := r.Store.ReviseJob(ctx, next); err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("revise job %d: %w", j.ID, err)
	}

	r.logFinished(next)

	return nil
}

// receipt records the SMSC's final word on a job's message, which the card's answer, if any, has not yet reached
// (SMPP 3.4 section 2.11): the job closes without a PoR, which a PoR that comes later still replaces. Asked for with
// registered_delivery 01, every receipt is final (section 5.2.17). It finds its job by receipted_message_id, which a
// receipt should carry (table 4-18), else by the id of its text (appendix B); its message_state, else the stat of its
// text, gives the reason.
func (r *Runner) receipt(ctx context.Context, d *smpp.DeliverSM) error {
	id := receiptField(d.ShortMessage, "id")
	if d.Options.ReceiptedMessageID != nil {
		id = *d.Options.ReceiptedMessageID
	}

	if id == "" {
		r.Logger.Info("ignoring a receipt without a message id", slog.String("text", string(d.ShortMessage)))
		return nil
	}

	j, err := r.Store.SentJob(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		r.Logger.Debug("ignoring a receipt for no job in progress", slog.String("message", id))
		return nil
	}

	if err != nil {
		return err
	}

	next := noPoR(j, receiptReason(d))
	next.PoRMissing = true

	return r.finish(ctx, next)
}

// receiptReason tells what a receipt says of its message.
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

// receiptState is the message_state of a receipt (SMPP 3.4 section 5.3.2.35), or the one its text's stat names, of
// the final states of SMPP 3.4 appendix B, table B-2.
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

// receiptField is the value of name in a receipt text of SMPP 3.4 appendix B: "id:IIIIIIIIII sub:SSS ... stat:DDDDDDD
// err:E Text:...", or empty.
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

// noPoR is the outcome of a command answered by no PoR: unconfirmed for a write, which the card may have applied,
// failed for a read.
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

// Outcome is job j after the PoR r, of user data packet, that answers its last command: the job with the next command
// to send, or final.
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
		// The card executed the command and sends the result later, by SMS-SUBMIT (TS 31.115 clauses 4.1 and 7,
		// TS 102 225 table 5), which does not reach simota.
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
		// Every command up to the read-back ran; the update was applied.
		return fail(db.JobUnconfirmed, "Written, but the read-back failed: %v.", err)
	}

	if err != nil {
		return fail(db.JobFailed, "%s", sentence(err))
	}

	j.Warning = res.Warning

	return advance(j, rr.Data)
}

// advance is the job after its current command answered data: the next command, or the end.
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

// Script is the remote file management script of a job's current command, for the card's ADF RFM application linked
// to the USIM, where ADF.USIM is the current directory at the start (TS 102 226 clause 7.3):
//
//   - a set or a clear writes its slot of EF.OPLMNwAcT and reads it back;
//   - an IMSI write writes the whole of EF.IMSI and reads it back, so that one PoR tells what the card holds. It sends
//     no REFRESH and leaves the location files that TS 31.102 annex A, note 1, advises updating with EF.IMSI: the UE
//     takes the IMSI at its next USIM initialisation;
//   - a read first gets the file's FCP for its size, then reads the file in chunks of ReadChunk octets.
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

// chunkLength is the number of octets the current read command asks for.
func chunkLength(j db.Job) int {
	return min(ReadChunk, j.FileSize-(j.Step-1)*ReadChunk)
}

// write is what a write job writes, and reads back: data at offset of a transparent EF, named what.
type write struct {
	file   uint16
	offset uint16
	data   []byte
	what   string
}

// writeOf is what a write job writes: for a set or a clear, its slot's entry of EF.OPLMNwAcT; for an IMSI write, the
// whole of EF.IMSI, so that no octet of a former, longer IMSI is left behind.
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

// contents is what b, of the octets that w writes, holds in words: an IMSI's digits, or an entry's PLMN as MCC/MNC and
// its access technologies; in hex when it does not decode.
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

// sentence is err as a sentence of a job's error.
func sentence(err error) string {
	return asSentence(err.Error())
}

// asSentence is a job's error as a sentence: capitalised, with a full stop.
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

// fail fails a queued job, for the runner, which logs a failure to store it: the job stays queued and is tried again.
func (r *Runner) fail(ctx context.Context, j db.Job, reason string) {
	j.Status, j.Error = db.JobFailed, asSentence(reason)
	if err := r.finish(ctx, j); err != nil {
		r.Logger.Error("failed to finish job", slog.Int64("job", j.ID), slog.Any("error", err))
	}
}

// finish records the outcome of a job in progress. A job already closed, by an earlier copy of the same deliver_sm
// for one, is not an error.
func (r *Runner) finish(ctx context.Context, j db.Job) error {
	err := r.Store.FinishJob(ctx, j)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("finish job %d: %w", j.ID, err)
	}

	r.logFinished(j)

	return nil
}

func (r *Runner) logFinished(j db.Job) {
	r.Logger.Info("job finished", slog.Int64("job", j.ID), slog.String("iccid", j.ICCID), slog.String("status", string(j.Status)),
		slog.String("error", j.Error))
}
