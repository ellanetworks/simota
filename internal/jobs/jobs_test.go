package jobs_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
	"github.com/ellanetworks/simota/internal/jobs"
	"github.com/ellanetworks/simota/internal/ota"
	"github.com/ellanetworks/smsc/smpp"
)

const iccid = "8949440000001659208"

var tar = []byte{0xb0, 0x00, 0x01}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// sja5 is key set 1 of the lab sysmoISIM-SJA5.
func sja5(t *testing.T) db.KeySet {
	return db.KeySet{
		Version: 1, Algorithm: "3des", KIc: mustHex(t, "2184A42A93F563DB248095A5F539D489"),
		KID: mustHex(t, "E45C662B528BB14A67F99FE70AB193C9"),
	}
}

func aesKeyset(t *testing.T) ota.Keyset {
	return ota.Keyset{
		Version: 2, Algorithm: ota.AES, KIc: mustHex(t, "200102030405060708090a0b0c0d0e0f"),
		KID: mustHex(t, "201102030405060708090a0b0c0d0e0f"),
	}
}

// fakeSMSC is Ella SMSC as the runner meets it over SMPP 3.4: a server on a loopback port that the runner's session
// binds to as a transceiver, which answers each submit_sm with the next message_id, or with the status the test sets,
// and sends the deliver_sm the test makes it send.
type fakeSMSC struct {
	srv *smpp.Server

	mu        sync.Mutex
	status    smpp.CommandStatus
	submitted []*smpp.SubmitSM
}

func newFakeSMSC(t *testing.T) (*fakeSMSC, int) {
	t.Helper()

	f := &fakeSMSC{}

	srv, err := smpp.NewServer(smpp.ServerConfig{
		SystemID: "EllaSMSC",
		Authenticate: func(_ context.Context, req smpp.BindRequest) smpp.BindOutcome {
			if req.Bind.ID != smpp.CmdBindTransceiver || req.Bind.SystemID != "simota" || req.Bind.Password != "secret" {
				return smpp.BindOutcome{Status: smpp.StatusBindFail}
			}

			return smpp.BindOutcome{Status: smpp.StatusOK}
		},
		Handler: smpp.HandlerFunc(func(_ context.Context, _ *smpp.Session, req smpp.Request) smpp.Response {
			f.mu.Lock()
			defer f.mu.Unlock()

			if f.status != smpp.StatusOK {
				return smpp.Response{Status: f.status}
			}

			f.submitted = append(f.submitted, req.PDU.(*smpp.SubmitSM))

			return smpp.Response{PDU: &smpp.SubmitSMResp{MessageID: strconv.Itoa(len(f.submitted))}}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = srv.Serve(l) }()

	t.Cleanup(func() { _ = srv.Close() })

	f.srv = srv

	return f, l.Addr().(*net.TCPAddr).Port
}

func (f *fakeSMSC) setStatus(s smpp.CommandStatus) {
	f.mu.Lock()
	f.status = s
	f.mu.Unlock()
}

func (f *fakeSMSC) sent() []*smpp.SubmitSM {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*smpp.SubmitSM{}, f.submitted...)
}

// deliver sends a deliver_sm to simota and waits for its deliver_sm_resp, which simota writes once it has recorded
// it.
func (f *fakeSMSC) deliver(t *testing.T, d *smpp.DeliverSM) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := f.srv.Deliver(ctx, "simota", d); err != nil {
		t.Fatalf("deliver_sm: %v", err)
	}
}

type env struct {
	store  *db.DB
	smsc   *fakeSMSC
	gw     *gateway.Gateway
	runner *jobs.Runner
	keyset ota.Keyset
	msisdn string
	now    time.Time
}

func newEnv(t *testing.T, msisdn string, keys db.KeySet) *env {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	card := db.Card{ICCID: iccid, IMSI: "001010000000001", MSISDN: msisdn, TAR: tar, KVN: keys.Version, KeySets: []db.KeySet{keys}}
	if err := store.CreateCard(context.Background(), card); err != nil {
		t.Fatal(err)
	}

	k := ota.Keyset{Version: uint8(keys.Version), Algorithm: ota.TripleDES, KIc: keys.KIc, KID: keys.KID, CCLength: keys.CCLength}
	if keys.Algorithm == "aes" {
		k.Algorithm = ota.AES
	}

	f, port := newFakeSMSC(t)
	e := &env{store: store, smsc: f, keyset: k, msisdn: msisdn, now: time.Now()}

	e.runner = jobs.NewRunner(store, func(context.Context) (jobs.Gateway, error) {
		return jobs.Gateway{SMSC: e.gw, Originator: "+15550001000", Validity: 24 * time.Hour}, nil
	}, time.Hour, slog.New(slog.DiscardHandler))
	e.runner.Now = func() time.Time { return e.now }

	e.gw = gateway.New(gateway.Config{Handler: e.runner, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	if err := e.gw.Apply(&gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "secret"}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = e.gw.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := e.gw.WaitBound(ctx); err != nil {
		t.Fatalf("not bound: %v, %+v", err, e.gw.State())
	}

	return e
}

func (e *env) queue(t *testing.T, j db.Job) int64 {
	t.Helper()

	j.ICCID = iccid

	ids, err := e.store.CreateJobs(context.Background(), []db.Job{j})
	if err != nil {
		t.Fatal(err)
	}

	return ids[0]
}

func (e *env) job(t *testing.T, id int64) db.Job {
	t.Helper()

	j, err := e.store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	return j
}

// simAnswer is the deliver_sm by which Ella SMSC forwards a card's PoR to the job's message: an SME Manual/User
// Acknowledgement with UDHI, the PoR with its user data header, and the message_id as receipted_message_id.
func (e *env) simAnswer(j db.Job, packet []byte) *smpp.DeliverSM {
	d := &smpp.DeliverSM{Message: smpp.Message{
		Source:      smpp.Address{TON: 1, NPI: 1, Addr: strings.TrimPrefix(e.msisdn, "+")},
		Destination: smpp.Address{TON: 1, NPI: 1, Addr: "15550001000"},
		ESMClass:    0x50, ProtocolID: 0x7f, DataCoding: 0xf6, ShortMessage: packet,
	}}
	d.Options.ReceiptedMessageID = new(j.MessageID)

	return d
}

// receipt is Ella SMSC's delivery receipt for the job's message (SMPP 3.4 section 2.11, appendix B).
func (e *env) receipt(j db.Job, state smpp.MessageState) *smpp.DeliverSM {
	d := &smpp.DeliverSM{Message: smpp.Message{
		Source:      smpp.Address{TON: 1, NPI: 1, Addr: strings.TrimPrefix(e.msisdn, "+")},
		Destination: smpp.Address{TON: 1, NPI: 1, Addr: "15550001000"},
		ESMClass:    0x04, DataCoding: 0x01,
		ShortMessage: []byte("id:" + j.MessageID + " sub:001 dlvrd:000 submit date:2610281200 done date:2610281201 " +
			"stat:" + stat[state] + " err:000 text:"),
	}}
	d.Options.ReceiptedMessageID = new(j.MessageID)
	d.Options.MessageState = new(state)

	return d
}

var stat = map[smpp.MessageState]string{
	smpp.StateDelivered: "DELIVRD", smpp.StateExpired: "EXPIRED", smpp.StateUndeliverable: "UNDELIV",
	smpp.StateDeleted: "DELETED", smpp.StateRejected: "REJECTD", smpp.StateEnroute: "ENROUTE",
}

// por is the PoR of the card to the job's current command, with status and the compact response data.
func (e *env) por(t *testing.T, id int64, status ota.Status, data string) []byte {
	t.Helper()

	j := e.job(t, id)
	r := ota.Response{Counter: j.Counter, Status: status, Data: mustHex(t, data)}
	copy(r.TAR[:], j.TAR)

	udh, ud, err := ota.EncodeResponse(e.keyset, jobs.SPI, r)
	if err != nil {
		t.Fatal(err)
	}

	return append(udh, ud...)
}

// answer makes the SMSC deliver the job's current command and forward the card's PoR, then its receipt DELIVRD, in
// that order, as Ella SMSC does.
func (e *env) answer(t *testing.T, id int64, status ota.Status, data string) {
	t.Helper()

	j := e.job(t, id)
	e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, status, data)))
	e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))
}

func (e *env) step() {
	e.runner.Step(context.Background())
}

// fcp is a compact response to SELECT for the FCP and GET RESPONSE: an EF of size octets.
func fcp(size int) string {
	return "02" + "9000" + "62" + "0b" + "82024121" + "83026f61" + "8001" + hex.EncodeToString([]byte{byte(size)})
}

func TestRead(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

	e.step()

	j := e.job(t, id)
	if j.Status != db.JobSent || j.Step != 0 || j.Counter != 1 || j.MessageID != "1" {
		t.Fatalf("after sending the FCP command, job = %+v", j)
	}

	// The submit_sm of SMPP 3.4 section 4.4.1: international E.164 addresses, UDHI, PID 7F, class 2 8-bit, a relative
	// validity of a day, a receipt and the SME Manual/User Acknowledgement asked for, the command packet behind its
	// user data header.
	b := e.smsc.sent()[0]
	if b.Source != (smpp.Address{TON: 1, NPI: 1, Addr: "15550001000"}) || b.Destination != (smpp.Address{TON: 1, NPI: 1, Addr: "15550000001"}) ||
		b.ESMClass != 0x40 || b.ProtocolID != 0x7f || b.DataCoding != 0xf6 || b.RegisteredDelivery != 0x09 ||
		b.ValidityPeriod != "000001000000000R" || b.ScheduleDeliveryTime != "" || !bytes.HasPrefix(b.ShortMessage, []byte{2, 0x70, 0}) ||
		!bytes.Equal(b.ShortMessage, j.Command) {
		t.Fatalf("submitted %+v", b)
	}

	// A pending message leaves the job sent.
	e.step()

	if j := e.job(t, id); j.Status != db.JobSent || len(e.smsc.sent()) != 1 {
		t.Fatalf("while pending, job = %+v", j)
	}

	e.answer(t, id, ota.StatusPoROK, fcp(60))
	e.step()

	j = e.job(t, id)
	if j.Status != db.JobSent || j.Step != 1 || j.FileSize != 60 || j.Counter != 2 || len(e.smsc.sent()) != 2 {
		t.Fatalf("after the FCP, job = %+v", j)
	}

	file := "00f110ffff" + strings.Repeat("ffffff0000", 11)
	e.answer(t, id, ota.StatusPoROK, "02"+"9000"+file)
	e.step()

	j = e.job(t, id)
	if j.Status != db.JobSucceeded || hex.EncodeToString(j.Data) != file || j.StatusWord != 0x9000 || j.PoRStatus != 0 {
		t.Fatalf("job = %+v", j)
	}
}

// TestReadLargeFile reads 255 octets in chunks that each fit a PoR.
func TestReadLargeFile(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

	e.step()
	e.answer(t, id, ota.StatusPoROK, fcp(255))
	e.step()

	var file []byte

	for i, n := range []int{125, 125, 5} {
		chunk := bytes.Repeat([]byte{byte(i + 1)}, n)
		file = append(file, chunk...)
		e.answer(t, id, ota.StatusPoROK, "02"+"9000"+hex.EncodeToString(chunk))
		e.step()
	}

	j := e.job(t, id)
	if j.Status != db.JobSucceeded || !bytes.Equal(j.Data, file) || len(e.smsc.sent()) != 4 || j.Counter != 4 {
		t.Fatalf("job = %+v after %d messages", j, len(e.smsc.sent()))
	}

	// Every PoR fits the TP-UD of an SMS-DELIVER-REPORT (TS 23.040 clause 9.2.2.1a).
	r := ota.Response{Counter: 9, Data: append([]byte{2, 0x90, 0}, bytes.Repeat([]byte{1}, jobs.ReadChunk)...)}
	copy(r.TAR[:], tar)

	for _, k := range []ota.Keyset{e.keyset, aesKeyset(t)} {
		udh, ud, err := ota.EncodeResponse(k, jobs.SPI, r)
		if err != nil || len(udh)+len(ud) > 159 {
			t.Fatalf("PoR of %d octets, %v", len(udh)+len(ud), err)
		}
	}
}

func TestReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		err    string
	}{
		{"not an FCP", "02" + "9000" + "6f00", "not an FCP template"},
		{"not entries", fcp(61), "is not a list of 5-octet entries"},
		{"file not found", "01" + "6a82", "command 1 of 2 failed with status word 6a82"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

			e.step()
			e.answer(t, id, ota.StatusPoROK, tc.answer)
			e.step()

			if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, tc.err) {
				t.Fatalf("job = %+v", j)
			}
		})
	}
}

func TestSetAndClear(t *testing.T) {
	entry := "99f9994800"
	clear2 := db.Job{Operation: db.OperationOPLMNClear, Slot: 2}

	for _, tc := range []struct {
		name   string
		job    db.Job
		answer string
		status db.JobStatus
		err    string
	}{
		{"set", db.Job{Operation: db.OperationOPLMNSet, Slot: 2, MCC: "999", MNC: "99", Access: 0x4800}, "03" + "9000" + entry, db.JobSucceeded, ""},
		{"clear", clear2, "03" + "9000" + "ffffff0000", db.JobSucceeded, ""},
		{"clear with a warning", clear2, "03" + "6282" + "ffffff0000", db.JobSucceeded, ""},
		{"read-back differs", clear2, "03" + "9000" + entry, db.JobFailed, "reads back"},
		{"update refused", clear2, "02" + "6982", db.JobFailed, "command 2 of 3"},
		{"read-back failed", clear2, "03" + "6f00", db.JobUnconfirmed, "written, but"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, tc.job)

			e.step()
			e.answer(t, id, ota.StatusPoROK, tc.answer)
			e.step()

			j := e.job(t, id)
			if j.Status != tc.status || !strings.Contains(j.Error, tc.err) || len(e.smsc.sent()) != 1 {
				t.Fatalf("job = %+v", j)
			}

			if strings.Contains(tc.name, "warning") && !j.Warning {
				t.Fatalf("job = %+v, want a warning", j)
			}
		})
	}
}

func TestPoRStatus(t *testing.T) {
	set := db.Job{Operation: db.OperationOPLMNClear, Slot: 2}
	read := db.Job{Operation: db.OperationOPLMNRead}

	for _, tc := range []struct {
		name   string
		job    db.Job
		status ota.Status
		want   db.JobStatus
		err    string
	}{
		{"write answered by SMS-SUBMIT", set, ota.StatusResponseInSubmit, db.JobUnconfirmed, "response_in_sms_submit"},
		{"write needing more time", set, ota.StatusMoreTime, db.JobUnconfirmed, "more_time"},
		{"read answered by SMS-SUBMIT", read, ota.StatusResponseInSubmit, db.JobFailed, "response_in_sms_submit"},
		{"counter low", set, ota.StatusCounterLow, db.JobFailed, "counter_low"},
		{"TAR unknown", set, ota.StatusTARUnknown, db.JobFailed, "no application at TAR b00001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, tc.job)

			e.step()
			e.answer(t, id, tc.status, "")
			e.step()

			if j := e.job(t, id); j.Status != tc.want || !strings.Contains(j.Error, tc.err) || j.PoRStatus != int(tc.status) {
				t.Fatalf("job = %+v", j)
			}
		})
	}
}

// TestPoRForAnotherCommand checks that a PoR is the answer to the job's command: its TAR, and its counter when
// secured, are the command's (TS 102 225 table 3).
func TestPoRForAnotherCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tar     []byte
		counter uint64
	}{
		{"other TAR", []byte{0xb0, 0x00, 0x20}, 1},
		{"other counter", tar, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

			e.step()

			r := ota.Response{Counter: tc.counter, Data: mustHex(t, "039000ffffff0000")}
			copy(r.TAR[:], tc.tar)

			udh, ud, err := ota.EncodeResponse(e.keyset, jobs.SPI, r)
			if err != nil {
				t.Fatal(err)
			}

			j := e.job(t, id)
			e.smsc.deliver(t, e.simAnswer(j, append(udh, ud...)))

			if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, "not this command's") {
				t.Fatalf("job = %+v", j)
			}
		})
	}
}

func TestNoPoR(t *testing.T) {
	clear2 := db.Job{Operation: db.OperationOPLMNClear, Slot: 2}
	read := db.Job{Operation: db.OperationOPLMNRead}
	unsecured := []byte{2, 0x71, 0, 0, 0x0b, 0x0a, 0xb0, 0, 1, 0, 0, 0, 0, 0, 0, 6}

	for _, tc := range []struct {
		name   string
		job    db.Job
		answer []byte // nil: no SIM answer
		state  smpp.MessageState
		want   db.JobStatus
		err    string
	}{
		{"write delivered", clear2, nil, smpp.StateDelivered, db.JobUnconfirmed, "cannot authenticate"},
		{"write expired", clear2, nil, smpp.StateExpired, db.JobUnconfirmed, "message expired"},
		{"write undeliverable", clear2, nil, smpp.StateUndeliverable, db.JobUnconfirmed, "message undeliverable"},
		{"read delivered", read, nil, smpp.StateDelivered, db.JobFailed, "without a PoR"},
		{"read rejected", read, nil, smpp.StateRejected, db.JobFailed, "rejected"},
		{"unsecured refusal", clear2, unsecured, smpp.StateDelivered, db.JobFailed, "unsecured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, tc.job)

			e.step()

			j := e.job(t, id)
			if tc.answer != nil {
				e.smsc.deliver(t, e.simAnswer(j, tc.answer))
			}

			e.smsc.deliver(t, e.receipt(j, tc.state))

			if j := e.job(t, id); j.Status != tc.want || !strings.Contains(j.Error, tc.err) {
				t.Fatalf("job = %+v", j)
			}
		})
	}
}

// TestReceiptNotFinal checks that ENROUTE leaves the job waiting (SMPP 3.4 section 5.2.28).
func TestReceiptNotFinal(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

	e.step()
	e.smsc.deliver(t, e.receipt(e.job(t, id), smpp.StateEnroute))

	if j := e.job(t, id); j.Status != db.JobSent {
		t.Fatalf("job = %+v", j)
	}
}

// TestCorrelation checks how a deliver_sm finds its job: by receipted_message_id; for a receipt without it, by the id
// of its text (SMPP 3.4 appendix B); for a SIM answer without it, by the card's number, the answer's source; and
// that one for another message is acknowledged and left aside.
func TestCorrelation(t *testing.T) {
	t.Run("answer without receipted_message_id or UDHI", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()

		d := e.simAnswer(e.job(t, id), e.por(t, id, ota.StatusPoROK, "039000ffffff0000"))
		d.Options.ReceiptedMessageID = nil
		d.ESMClass = 0x10 // SME Manual/User Acknowledgement, no UDHI: the PoR is read as if it were set (TS 31.115 4.1).
		e.smsc.deliver(t, d)

		if j := e.job(t, id); j.Status != db.JobSucceeded {
			t.Fatalf("job = %+v", j)
		}
	})

	t.Run("receipt without TLVs", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		d := e.receipt(e.job(t, id), smpp.StateExpired)
		d.Options = smpp.Options{}
		e.smsc.deliver(t, d)

		if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, "expired") {
			t.Fatalf("job = %+v", j)
		}
	})

	t.Run("deliver_sm for another message", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		j := e.job(t, id)
		j.MessageID = "999"
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))
		e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusPoROK, fcp(60))))

		// Neither a normal message.
		e.smsc.deliver(t, &smpp.DeliverSM{Message: smpp.Message{Source: smpp.Address{Addr: "15550000001"}, ShortMessage: []byte("hi")}})

		if j := e.job(t, id); j.Status != db.JobSent {
			t.Fatalf("job = %+v", j)
		}
	})
}

// TestNoReceipt checks that a job sent long past its validity, without a receipt, ends.
func TestNoReceipt(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()

	e.now = e.now.Add(24 * time.Hour)
	e.step()

	if j := e.job(t, id); j.Status != db.JobSent {
		t.Fatalf("within the grace, job = %+v", j)
	}

	e.now = e.now.Add(2 * time.Hour)
	e.step()

	if j := e.job(t, id); j.Status != db.JobUnconfirmed || !strings.Contains(j.Error, "no delivery receipt") {
		t.Fatalf("job = %+v", j)
	}
}

// TestAES runs a job on an AES key set with 4-octet checksums, which needs the counter (TS 102 225 clause 5.1.2).
func TestAES(t *testing.T) {
	k := aesKeyset(t)
	e := newEnv(t, "+15550000001", db.KeySet{Version: 2, Algorithm: "aes", KIc: k.KIc, KID: k.KID, CCLength: 4})
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()
	e.answer(t, id, ota.StatusPoROK, "039000ffffff0000")
	e.step()

	if j := e.job(t, id); j.Status != db.JobSucceeded {
		t.Fatalf("job = %+v", j)
	}
}

func TestSendFailures(t *testing.T) {
	t.Run("no MSISDN", func(t *testing.T) {
		e := newEnv(t, "", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
		e.step()

		if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, "no MSISDN") || len(e.smsc.sent()) != 0 {
			t.Fatalf("job = %+v", j)
		}
	})

	t.Run("SMSC refuses", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.smsc.setStatus(smpp.StatusInvDstAdr)
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
		e.step()

		if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, "ESME_RINVDSTADR") {
			t.Fatalf("job = %+v", j)
		}
	})

	t.Run("SMSC busy", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.smsc.setStatus(smpp.StatusThrottled)
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
		e.step()

		if j := e.job(t, id); j.Status != db.JobQueued {
			t.Fatalf("job = %+v, want it queued for a retry", j)
		}

		// The retry takes a new counter: the spent one may have reached the card.
		e.smsc.setStatus(smpp.StatusOK)
		e.step()

		if j := e.job(t, id); j.Status != db.JobSent || j.Counter != 2 {
			t.Fatalf("after the SMSC came back, job = %+v", j)
		}
	})

	t.Run("not bound", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		if err := e.gw.Apply(nil); err != nil {
			t.Fatal(err)
		}

		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
		e.step()

		// The job waits, and spends no counter.
		if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 0 {
			t.Fatalf("job = %+v", j)
		}

		if n, err := e.store.NextCounter(context.Background(), iccid, 1); err != nil || n != 1 {
			t.Fatalf("next counter = %d, %v", n, err)
		}
	})
}

func TestRejected(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&smpp.StatusError{Command: smpp.CmdSubmitSMResp, Status: smpp.StatusInvDstAdr}, true},
		{&smpp.StatusError{Command: smpp.CmdGenericNack, Status: smpp.StatusInvCmdID}, true},
		{&smpp.StatusError{Command: smpp.CmdSubmitSMResp, Status: smpp.StatusThrottled}, false},
		{&smpp.StatusError{Command: smpp.CmdSubmitSMResp, Status: smpp.StatusMsgQFul}, false},
		{&smpp.StatusError{Command: smpp.CmdSubmitSMResp, Status: smpp.StatusSysErr}, false},
		{smpp.ErrResponseTimeout, false},
		{smpp.ErrClosed, false},
	} {
		if got := jobs.Rejected(tc.err); got != tc.want {
			t.Errorf("Rejected(%v) = %v", tc.err, got)
		}
	}
}

func TestValidityPeriod(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		validity time.Duration
		want     string
	}{
		{24 * time.Hour, "000001000000000R"},
		{90 * time.Minute, "000000013000000R"},
		{time.Second, "000000000001000R"},
		{103 * 24 * time.Hour, "000099960000000R"},
		// Beyond 99 days and 99 hours, an absolute time in UTC (SMPP 3.4 section 7.1.1.1).
		{63 * 7 * 24 * time.Hour, "280112120000000+"},
	} {
		got, err := jobs.ValidityPeriod(tc.validity, now)
		if err != nil || got != tc.want {
			t.Errorf("ValidityPeriod(%v) = %q, %v, want %q", tc.validity, got, err, tc.want)
			continue
		}

		// Ella SMSC reads it back to the same expiry.
		vp, err := smpp.ParseTime(got, now)
		if err != nil || !vp.At(now).Equal(now.Add(tc.validity)) {
			t.Errorf("%q reads as %v, %v", got, vp.At(now), err)
		}
	}
}

func TestScript(t *testing.T) {
	for _, tc := range []struct {
		job  db.Job
		want string
	}{
		{db.Job{Operation: db.OperationOPLMNRead}, "00a40004026f6100c0000000"},
		{db.Job{Operation: db.OperationOPLMNRead, Step: 2, FileSize: 255}, "00a4000c026f6100b0007d7d"},
		{db.Job{Operation: db.OperationOPLMNRead, Step: 3, FileSize: 255}, "00a4000c026f6100b000fa05"},
		{
			db.Job{Operation: db.OperationOPLMNSet, Slot: 2, MCC: "999", MNC: "99", Access: 0x4800},
			"00a4000c026f6100d600050599f999480000b0000505",
		},
		{db.Job{Operation: db.OperationOPLMNClear, Slot: 2}, "00a4000c026f6100d6000505ffffff000000b0000505"},
	} {
		s, err := jobs.Script(tc.job)
		if err != nil {
			t.Fatalf("%+v: %v", tc.job, err)
		}

		got, err := s.Bytes()
		if err != nil {
			t.Fatalf("%+v: %v", tc.job, err)
		}

		if hex.EncodeToString(got) != tc.want {
			t.Fatalf("%+v = %x, want %s", tc.job, got, tc.want)
		}
	}

	for _, j := range []db.Job{
		{Operation: "oplmn_delete"},
		{Operation: db.OperationOPLMNClear, Slot: 0},
		{Operation: db.OperationOPLMNSet, Slot: 52, MCC: "999", MNC: "99"},
		{Operation: db.OperationOPLMNSet, Slot: 2, MCC: "99", MNC: "99"},
		{Operation: db.OperationOPLMNRead, Step: 1, FileSize: -1},
		{Operation: db.OperationOPLMNRead, Step: 4, FileSize: 255},
	} {
		if _, err := jobs.Script(j); err == nil {
			t.Fatalf("%+v: expected an error", j)
		}
	}
}

func TestOneJobPerCard(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

	_, err := e.store.CreateJobs(context.Background(), []db.Job{{ICCID: iccid, Operation: db.OperationOPLMNRead}})
	if err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
}

// TestRunWakes checks that Run sends a job as soon as it is woken, long before its interval.
func TestRunWakes(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		e.runner.Run(ctx)
		close(done)
	}()

	defer func() {
		cancel()
		<-done
	}()

	id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
	e.runner.Wake()

	deadline := time.Now().Add(5 * time.Second)
	for e.job(t, id).Status != db.JobSent {
		if time.Now().After(deadline) {
			t.Fatal("the woken runner did not send the job")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// TestNoGateway checks that jobs wait, queued, while no SMSC is set.
func TestNoGateway(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

	set := false
	e.runner.Connect = func(context.Context) (jobs.Gateway, error) {
		if !set {
			return jobs.Gateway{}, jobs.ErrNoGateway
		}

		return jobs.Gateway{SMSC: e.gw, Originator: "+15550001000", Validity: time.Hour}, nil
	}

	e.step()

	if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 0 {
		t.Fatalf("without an SMSC, job = %+v", j)
	}

	set = true

	e.step()

	if j := e.job(t, id); j.Status != db.JobSent || e.smsc.sent()[0].ValidityPeriod != "000000010000000R" {
		t.Fatalf("with an SMSC, job = %+v", j)
	}
}
