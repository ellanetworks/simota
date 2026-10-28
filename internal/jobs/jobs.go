// Package jobs runs OTA jobs: it sends each queued job's commands to its card through Ella SMSC over SMPP 3.4, one at
// a time, and records the card's answers and the SMSC's delivery receipts that the SMSC pushes back as deliver_sm.
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

// SPI is the security of the commands and of the PoRs: ciphering, a cryptographic checksum, and a counter that must
// be higher than the card's, which protects against replay and which AES requires (TS 102 225 clauses 5.1.1, 5.1.2);
// a ciphered PoR with a cryptographic checksum in the SMS-DELIVER-REPORT.
var SPI = ota.SPI{
	Integrity:    ota.IntegrityCC,
	Ciphering:    true,
	Counter:      ota.CounterHigher,
	PoR:          ota.PoRAlways,
	PoRIntegrity: ota.IntegrityCC,
	PoRCiphering: true,
}

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
	// RegisteredDelivery asks for an SME Manual/User Acknowledgement, bits 3-2 = 10, which carries the card's PoR, and
	// for an SMSC delivery receipt on success or failure, bits 1-0 = 01 (SMPP 3.4 section 5.2.17).
	RegisteredDelivery = 0x09
)

// The message types of a deliver_sm's esm_class, bits 5-2 (SMPP 3.4 section 5.2.12).
const (
	messageTypeReceipt = 0x1
	messageTypeUserAck = 0x4
)

// receiptGrace is how long past the validity a sent job waits for its receipt: the SMSC sends it when it gives up on
// the message, and again after a failed attempt to send it.
const receiptGrace = time.Hour

type Store interface {
	GetCard(ctx context.Context, iccid string) (db.Card, error)
	NextCounter(ctx context.Context, iccid string, version int) (uint64, error)
	JobsIn(ctx context.Context, status db.JobStatus, limit int) ([]db.Job, error)
	SentJob(ctx context.Context, messageID string) (db.Job, error)
	SentJobTo(ctx context.Context, msisdn string) (db.Job, error)
	MarkJobSent(ctx context.Context, id int64, counter uint64, messageID string, command []byte) error
	AdvanceJob(ctx context.Context, j db.Job) error
	FinishJob(ctx context.Context, j db.Job) error
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
	// mu orders a submit_sm and the recording of its message_id before any deliver_sm about it is handled.
	mu sync.Mutex
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

// Step closes the sent jobs that no receipt answered in time, then sends the queued ones. A job whose submit_sm
// fails without the SMSC refusing it stays queued, for the next step.
func (r *Runner) Step(ctx context.Context) {
	gw, err := r.Connect(ctx)
	if errors.Is(err, ErrNoGateway) {
		return
	}

	if err != nil {
		r.Logger.Error("failed to get the SMSC", slog.Any("error", err))
		return
	}

	sent, err := r.Store.JobsIn(ctx, db.JobSent, batch)
	if err != nil {
		r.Logger.Error("failed to list sent jobs", slog.Any("error", err))
		return
	}

	for _, j := range sent {
		if r.now().Sub(j.UpdatedAt) > gw.Validity+receiptGrace {
			r.finish(ctx, noPoR(j, "no delivery receipt from the SMSC within the validity of the message"))
		}
	}

	queued, err := r.Store.JobsIn(ctx, db.JobQueued, batch)
	if err != nil {
		r.Logger.Error("failed to list queued jobs", slog.Any("error", err))
		return
	}

	if len(queued) > 0 && !gw.SMSC.Bound() {
		r.Logger.Debug("not bound to the SMSC, jobs wait", slog.Int("queued", len(queued)))
		return
	}

	for _, j := range queued {
		if !r.send(ctx, gw, j) {
			return
		}
	}
}

// send sends a queued job's current command, and reports whether the next job may be sent: not while unbound.
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

	cmd := ota.Command{Keyset: keyset, SPI: SPI, Counter: counter, Data: data}
	copy(cmd.TAR[:], j.TAR)

	udh, ud, err := cmd.Encode()
	if err != nil {
		r.fail(ctx, j, err.Error())
		return true
	}

	sm, err := SubmitSM(gw.Originator, card.MSISDN, gw.Validity, append(udh, ud...), r.now())
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
		r.Logger.Warn("failed to submit job, will retry", slog.Int64("job", j.ID), slog.Any("error", err))
		return true
	}

	if err := r.Store.MarkJobSent(ctx, j.ID, counter, id, sm.ShortMessage); err != nil {
		r.Logger.Error("failed to mark job sent", slog.Int64("job", j.ID), slog.String("message", id), slog.Any("error", err))
	}

	return true
}

// SubmitSM is the submit_sm of a command packet, user data header included, from originator to the card's msisdn,
// both E.164 with a leading "+", valid for validity from now.
func SubmitSM(originator, msisdn string, validity time.Duration, packet []byte, now time.Time) (*smpp.SubmitSM, error) {
	vp, err := ValidityPeriod(validity, now)
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

// ValidityPeriod is the validity_period of a validity: relative (SMPP 3.4 section 7.1.1.2), in days, hours, minutes
// and seconds of at most 99 each, or, for a longer one, absolute in UTC (section 7.1.1.1), from now.
func ValidityPeriod(validity time.Duration, now time.Time) (string, error) {
	rest := validity.Round(time.Second)

	var p smpp.RelativePeriod

	for _, f := range []struct {
		n    *int
		unit time.Duration
	}{{&p.Days, 24 * time.Hour}, {&p.Hours, time.Hour}, {&p.Minutes, time.Minute}, {&p.Seconds, time.Second}} {
		*f.n = int(min(rest/f.unit, 99))
		rest -= time.Duration(*f.n) * f.unit
	}

	if rest == 0 {
		return smpp.FormatRelative(p)
	}

	return smpp.FormatAbsolute(now.Add(validity).UTC())
}

// Rejected reports whether err is the SMSC refusing a submit_sm, which sending it again would not change, rather
// than failing to answer it: any command_status but a throttling, a full queue or a system error (SMPP 3.4 section
// 5.1.3, table 5-2).
func Rejected(err error) bool {
	var se *smpp.StatusError
	if !errors.As(err, &se) {
		return false
	}

	switch se.Status {
	case smpp.StatusThrottled, smpp.StatusMsgQFul, smpp.StatusSysErr:
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

	for _, k := range card.KeySets {
		if k.Version != j.KVN {
			continue
		}

		keyset := ota.Keyset{Version: uint8(k.Version), Algorithm: ota.TripleDES, KIc: k.KIc, KID: k.KID, CCLength: k.CCLength}
		if k.Algorithm == "aes" {
			keyset.Algorithm = ota.AES
		}

		return card, keyset, nil
	}

	return db.Card{}, ota.Keyset{}, fmt.Errorf("the card has no key set %d", j.KVN)
}

// ServeSMPP takes the SMSC's deliver_sm: the card's answer, an SME Manual/User Acknowledgement, or the SMSC's
// delivery receipt (SMPP 3.4 sections 2.11 and 5.2.12). It answers ESME_RX_T_APPN when it cannot record the outcome,
// so that the SMSC sends the deliver_sm again later, and ESME_ROK otherwise, for a deliver_sm of no job too.
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

// Deliver records a deliver_sm on the job it is about.
func (r *Runner) Deliver(ctx context.Context, d *smpp.DeliverSM) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch d.ESMClass >> 2 & 0x0f {
	case messageTypeUserAck:
		return r.answer(ctx, d)
	case messageTypeReceipt:
		return r.receipt(ctx, d)
	default:
		r.Logger.Info("ignoring a deliver_sm that is neither a SIM answer nor a receipt", slog.Int("esm_class", int(d.ESMClass)),
			slog.String("source", d.Source.Addr))

		return nil
	}
}

// jobOf is the sent job that a deliver_sm is about: the one whose message_id is the deliver_sm's
// receipted_message_id, or, for a receipt without it, the id of its text (SMPP 3.4 appendix B). Without either, a SIM
// answer is the current command's of its card, whose number is the answer's source (SMPP 3.4 section 2.11); the PoR's
// TAR and counter then tell whether it answers that command.
func (r *Runner) jobOf(ctx context.Context, d *smpp.DeliverSM, answer bool) (db.Job, error) {
	id := ""

	switch {
	case d.Options.ReceiptedMessageID != nil:
		id = *d.Options.ReceiptedMessageID
	case !answer:
		id = receiptField(d.ShortMessage, "id")
	}

	if id != "" {
		return r.Store.SentJob(ctx, id)
	}

	if answer && d.Source.Addr != "" {
		return r.Store.SentJobTo(ctx, "+"+strings.TrimPrefix(d.Source.Addr, "+"))
	}

	return db.Job{}, db.ErrNotFound
}

// answer records the card's answer to a job's command: its short_message is the Response Packet, with its user data
// header when UDHI is set (TS 31.115 clause 4.1).
func (r *Runner) answer(ctx context.Context, d *smpp.DeliverSM) error {
	j, err := r.jobOf(ctx, d, true)
	if errors.Is(err, db.ErrNotFound) {
		r.Logger.Info("ignoring a SIM answer for no job in progress", slog.String("source", d.Source.Addr))
		return nil
	}

	if err != nil {
		return err
	}

	sm := d.ShortMessage
	if len(sm) == 0 {
		sm = d.Options.MessagePayload
	}

	udh, ud := []byte(nil), sm
	if d.ESMClass&ESMClassUDHI != 0 && len(sm) > 0 && int(sm[0]) < len(sm) {
		udh, ud = sm[:1+int(sm[0])], sm[1+int(sm[0]):]
	}

	_, keyset, err := r.cardOf(ctx, j)
	if err != nil {
		r.finish(ctx, noPoR(j, err.Error()))
		return nil
	}

	next, final := Outcome(j, keyset, udh, ud)
	if final {
		r.finish(ctx, next)
		return nil
	}

	if err := r.Store.AdvanceJob(ctx, next); err != nil {
		return fmt.Errorf("advance job %d: %w", j.ID, err)
	}

	r.Wake()

	return nil
}

// receipt records the SMSC's final word on a job's message, which no SIM answer preceded: the SMSC sends the answer
// first. Its message_state, or the stat of its text (SMPP 3.4 appendix B), tells whether the message was delivered.
func (r *Runner) receipt(ctx context.Context, d *smpp.DeliverSM) error {
	state, ok := receiptState(d)
	if !ok {
		r.Logger.Info("ignoring a receipt without a message state", slog.String("text", string(d.ShortMessage)))
		return nil
	}

	var reason string

	switch state {
	case smpp.StateDelivered:
		reason = "delivered without a PoR: the card answers none to a sender it cannot authenticate, such as with " +
			"other keys or key set; or the serving node passed none on"
	case smpp.StateExpired:
		reason = "message expired without a PoR"
	case smpp.StateUndeliverable:
		reason = "message undeliverable without a PoR"
	case smpp.StateDeleted:
		reason = "message deleted by the SMSC without a PoR"
	case smpp.StateRejected:
		reason = "message rejected by the SMSC without a PoR"
	case smpp.StateUnknown:
		reason = "message in an unknown state, without a PoR"
	default:
		// ENROUTE and ACCEPTED are not final (SMPP 3.4 section 5.2.28).
		return nil
	}

	j, err := r.jobOf(ctx, d, false)
	if errors.Is(err, db.ErrNotFound) {
		r.Logger.Debug("ignoring a receipt for no job in progress", slog.String("state", state.String()))
		return nil
	}

	if err != nil {
		return err
	}

	r.finish(ctx, noPoR(j, reason))

	return nil
}

// receiptState is the message_state of a receipt (SMPP 3.4 section 5.3.2.35), or the one its text's stat names in the
// format of SMPP 3.4 appendix B, table B-2.
func receiptState(d *smpp.DeliverSM) (smpp.MessageState, bool) {
	if s := d.Options.MessageState; s != nil {
		return *s, true
	}

	states := map[string]smpp.MessageState{
		"DELIVRD": smpp.StateDelivered, "EXPIRED": smpp.StateExpired, "DELETED": smpp.StateDeleted,
		"UNDELIV": smpp.StateUndeliverable, "ACCEPTD": smpp.StateAccepted, "UNKNOWN": smpp.StateUnknown,
		"REJECTD": smpp.StateRejected, "ENROUTE": smpp.StateEnroute,
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
	j.Status, j.Error = db.JobFailed, reason
	if writes(j) {
		j.Status = db.JobUnconfirmed
	}

	return j
}

func writes(j db.Job) bool {
	return j.Operation == db.OperationOPLMNSet || j.Operation == db.OperationOPLMNClear
}

// Outcome decodes the answer of a card to job j's current command, from the user data of its delivery report: the
// job with the next command to send, or final.
func Outcome(j db.Job, k ota.Keyset, udh, ud []byte) (db.Job, bool) {
	j.Response = append(append([]byte{}, udh...), ud...)
	j.Warning = false

	fail := func(status db.JobStatus, format string, a ...any) (db.Job, bool) {
		j.Status, j.Error = status, fmt.Sprintf(format, a...)
		return j, true
	}

	r, err := ota.DecodeResponse(k, SPI, udh, ud)
	if err != nil {
		return fail(db.JobFailed, "invalid PoR: %v", err)
	}

	// The PoR's TAR, and CNTR when secured, are copies of the command's (TS 102 225 clause 5.2, table 3).
	if !bytes.Equal(r.TAR[:], j.TAR) || !r.Unsecured && r.Counter != j.Counter {
		return fail(db.JobFailed, "PoR for TAR %x and counter %d, not this command's", r.TAR, r.Counter)
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

		return fail(status, "the card sends its result later, by SMS-SUBMIT: %s", r.Status)
	case ota.StatusTARUnknown:
		return fail(db.JobFailed, "the card has no application at TAR %x: set the card's ADF RFM TAR", j.TAR)
	default:
		if r.Unsecured {
			return fail(db.JobFailed, "the card refused the command, in an unsecured PoR: %s", r.Status)
		}

		return fail(db.JobFailed, "the card refused the command: %s", r.Status)
	}

	rr, err := rfm.DecodeResponse(r.Data)
	if err != nil {
		return fail(db.JobFailed, "%v", err)
	}

	j.StatusWord = int(rr.StatusWord)

	script, err := Script(j)
	if err != nil {
		return fail(db.JobFailed, "%v", err)
	}

	res, err := script.Check(rr)

	var se *rfm.ScriptError
	if errors.As(err, &se) && writes(j) && se.Executed == script.Len() {
		// Every command up to the read-back ran; the update was applied.
		return fail(db.JobUnconfirmed, "written, but the read-back failed: %v", err)
	}

	if err != nil {
		return fail(db.JobFailed, "%v", err)
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
		want, err := entryOf(j)
		if err != nil {
			return fail("%v", err)
		}

		j.Data = data
		if !bytes.Equal(data, want) {
			return fail("slot %d reads back %x, not the %x written", j.Slot, data, want)
		}

		j.Status = db.JobSucceeded

		return j, true
	}

	if j.Step == 0 {
		size, err := rfm.FileSize(data)
		if err != nil {
			return fail("%v", err)
		}

		if size%entryLength != 0 {
			return fail("EF.OPLMNwAcT of %d octets is not a list of %d-octet entries", size, entryLength)
		}

		j.FileSize, j.Data = size, nil
	} else {
		if want := chunkLength(j); len(data) != want {
			return fail("read %d octets, not %d", len(data), want)
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
//   - a read first gets the file's FCP for its size, then reads the file in chunks of ReadChunk octets.
func Script(j db.Job) (*rfm.Script, error) {
	s := rfm.NewScript(rfm.ClassUICC)

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
	case db.OperationOPLMNSet, db.OperationOPLMNClear:
		entry, err := entryOf(j)
		if err != nil {
			return nil, err
		}

		offset := rfm.SlotOffset(j.Slot)

		return s.Select(rfm.FileOPLMNwAcT).UpdateBinary(offset, entry).ReadBinary(offset, entryLength), nil
	default:
		return nil, fmt.Errorf("unknown operation %q", j.Operation)
	}
}

// chunkLength is the number of octets the current read command asks for.
func chunkLength(j db.Job) int {
	return min(ReadChunk, j.FileSize-(j.Step-1)*ReadChunk)
}

// entryOf is the entry a set or a clear writes.
func entryOf(j db.Job) ([]byte, error) {
	if j.Slot < 1 || j.Slot > MaxSlot {
		return nil, fmt.Errorf("slot %d not in 1-%d", j.Slot, MaxSlot)
	}

	e := rfm.Empty
	if j.Operation == db.OperationOPLMNSet {
		e = rfm.SelectorEntry{MCC: j.MCC, MNC: j.MNC, Access: rfm.AccessTechnology(j.Access)}
	}

	return e.Encode()
}

func (r *Runner) fail(ctx context.Context, j db.Job, reason string) {
	j.Status, j.Error = db.JobFailed, reason
	r.finish(ctx, j)
}

func (r *Runner) finish(ctx context.Context, j db.Job) {
	if err := r.Store.FinishJob(ctx, j); err != nil {
		r.Logger.Error("failed to finish job", slog.Int64("job", j.ID), slog.Any("error", err))
		return
	}

	r.Logger.Info("job finished", slog.Int64("job", j.ID), slog.String("iccid", j.ICCID), slog.String("status", string(j.Status)),
		slog.String("error", j.Error))
}
