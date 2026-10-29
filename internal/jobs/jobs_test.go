package jobs_test

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/des"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
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

// porPacket is the user data of the card's PoR r, its UDH then its Response Packet, secured with the triple DES key
// set k as ota.SPI asks: a cryptographic checksum, ciphered (TS 102 225 clause 5.2, TS 31.115 clause 4.4). It is coded
// apart from package ota, which only decodes PoRs.
func porPacket(t *testing.T, k ota.Keyset, r ota.Response) []byte {
	t.Helper()

	if k.Algorithm != ota.TripleDES {
		t.Fatal("porPacket codes triple DES PoRs only")
	}

	// CBC with a zero IV and two-key triple DES, K1 K2 K1, of data padded with zeros to the block.
	cbc := func(key, data []byte) []byte {
		b, err := des.NewTripleDESCipher(slices.Concat(key, key[:8]))
		if err != nil {
			t.Fatal(err)
		}

		data = append(data, make([]byte, (des.BlockSize-len(data)%des.BlockSize)%des.BlockSize)...)
		out := make([]byte, len(data))
		cipher.NewCBCEncrypter(b, make([]byte, des.BlockSize)).CryptBlocks(out, data)

		return out
	}

	const ccLength = 8

	udh := []byte{0x02, 0x71, 0x00}
	padding := (des.BlockSize - (5+2+ccLength+len(r.Data))%des.BlockSize) % des.BlockSize
	data := append(append([]byte{}, r.Data...), make([]byte, padding)...)
	counter := []byte{
		byte(r.Counter >> 32), byte(r.Counter >> 24), byte(r.Counter >> 16), byte(r.Counter >> 8), byte(r.Counter),
		byte(padding), byte(r.Status),
	}
	rpl := 1 + 3 + len(counter) + ccLength + len(data)
	header := []byte{byte(rpl >> 8), byte(rpl), 10 + ccLength, r.TAR[0], r.TAR[1], r.TAR[2]}

	// The CC covers the UDH, RPL, RHL, TAR, CNTR, PCNTR, the status and the data; ciphering all but the header.
	mac := cbc(k.KID, slices.Concat(udh, header, counter, data))

	return slices.Concat(udh, header, cbc(k.KIc, slices.Concat(counter, mac[len(mac)-ccLength:], data)))
}

// fakeSMSC is an SMSC as the runner meets it over SMPP 3.4: a server on a loopback port that the runner's session
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

	if err := f.try(d); err != nil {
		t.Fatalf("deliver_sm: %v", err)
	}
}

// try sends a deliver_sm and returns the error of its deliver_sm_resp: a *smpp.StatusError for a status other than
// ESME_ROK.
func (f *fakeSMSC) try(d *smpp.DeliverSM) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.srv.Deliver(ctx, "simota", d)

	return err
}

// faultyStore is the store with the failures a test sets.
type faultyStore struct {
	*db.DB

	mu                     sync.Mutex
	finish, card, markSent error
}

func (s *faultyStore) set(f func(s *faultyStore)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

func (s *faultyStore) fault(e *error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return *e
}

func (s *faultyStore) FinishJob(ctx context.Context, j db.Job) error {
	if err := s.fault(&s.finish); err != nil {
		return err
	}

	return s.DB.FinishJob(ctx, j)
}

func (s *faultyStore) GetCard(ctx context.Context, iccid string) (db.Card, error) {
	if err := s.fault(&s.card); err != nil {
		return db.Card{}, err
	}

	return s.DB.GetCard(ctx, iccid)
}

func (s *faultyStore) MarkJobSent(ctx context.Context, id int64, counter uint64, messageID string, command []byte) error {
	if err := s.fault(&s.markSent); err != nil {
		return err
	}

	return s.DB.MarkJobSent(ctx, id, counter, messageID, command)
}

var errDB = errors.New("database is locked")

type env struct {
	store  *db.DB
	faults *faultyStore
	smsc   *fakeSMSC
	gw     *gateway.Gateway
	runner *jobs.Runner
	keyset ota.Keyset
	msisdn string
	now    time.Time
	// validity is the gateway's.
	validity time.Duration
	settings gateway.Settings
	// generic makes the SMSC forward the card's answer as pySim-smpp2sim does, a normal message without UDHI or TLVs,
	// and send no receipt; otherwise as Ella SMSC does, an SME Manual/User Acknowledgement with UDHI and
	// receipted_message_id, then a receipt.
	generic bool
}

// forms runs a test with the SIM answers of both forms.
func forms(t *testing.T, run func(t *testing.T, generic bool)) {
	t.Run("ella", func(t *testing.T) { run(t, false) })
	t.Run("generic", func(t *testing.T) { run(t, true) })
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
	e := &env{
		store: store, faults: &faultyStore{DB: store}, smsc: f, keyset: k, msisdn: msisdn, now: time.Now(),
		validity: 24 * time.Hour,
	}

	e.runner = jobs.NewRunner(e.faults, func(context.Context) (jobs.Gateway, error) {
		return jobs.Gateway{SMSC: e.gw, Originator: "+15550001000", Validity: e.validity}, nil
	}, time.Hour, slog.New(slog.DiscardHandler))
	e.runner.Now = func() time.Time { return e.now }

	e.settings = gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "secret"}
	e.gw = gateway.New(gateway.Config{Handler: e.runner})

	t.Cleanup(func() { _ = e.gw.Close(context.Background()) })

	e.rebind(t)

	return e
}

// rebind binds the session to the SMSC and waits until it is bound.
func (e *env) rebind(t *testing.T) {
	t.Helper()

	if err := e.gw.Apply(new(e.settings)); err != nil {
		t.Fatal(err)
	}

	for deadline := time.Now().Add(5 * time.Second); !e.gw.Bound(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not bound: %+v", e.gw.State())
		}
	}
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

// simAnswer is the deliver_sm by which the SMSC forwards a card's PoR, with its user data header, to the job's
// message. Ella SMSC sends an SME Manual/User Acknowledgement with UDHI and the message_id as receipted_message_id;
// pySim-smpp2sim a normal message without UDHI or TLVs.
func (e *env) simAnswer(j db.Job, packet []byte) *smpp.DeliverSM {
	d := &smpp.DeliverSM{Message: smpp.Message{
		Source:      smpp.Address{TON: 1, NPI: 1, Addr: strings.TrimPrefix(e.msisdn, "+")},
		Destination: smpp.Address{TON: 1, NPI: 1, Addr: "15550001000"},
		ESMClass:    0x00, ProtocolID: 0x7f, DataCoding: 0xf6, ShortMessage: packet,
	}}

	if !e.generic {
		d.ESMClass = 0x50
		d.Options.ReceiptedMessageID = new(j.MessageID)
	}

	return d
}

// receipt is the SMSC's delivery receipt for the job's message (SMPP 3.4 section 2.11, appendix B).
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
	smpp.StateDeleted: "DELETED", smpp.StateRejected: "REJECTD", smpp.StateAccepted: "ACCEPTD",
}

// por is the PoR of the card to the job's current command, with status and the compact response data.
func (e *env) por(t *testing.T, id int64, status ota.Status, data string) []byte {
	t.Helper()

	j := e.job(t, id)

	return e.porOf(t, j.TAR, j.Counter, status, data)
}

// porOf is a PoR of the card for TAR tar and counter counter.
func (e *env) porOf(t *testing.T, tar []byte, counter uint64, status ota.Status, data string) []byte {
	t.Helper()

	r := ota.Response{Counter: counter, Status: status, Data: mustHex(t, data)}
	copy(r.TAR[:], tar)

	return porPacket(t, e.keyset, r)
}

// answer makes the SMSC deliver the job's current command and forward the card's PoR, then, as Ella SMSC does, its
// receipt DELIVRD.
func (e *env) answer(t *testing.T, id int64, status ota.Status, data string) {
	t.Helper()

	j := e.job(t, id)
	e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, status, data)))

	if !e.generic {
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))
	}
}

func (e *env) step() {
	e.runner.Step(context.Background())
}

// fcp is a compact response to SELECT for the FCP and GET RESPONSE: an EF of size octets.
func fcp(size int) string {
	return "02" + "9000" + "62" + "0b" + "82024121" + "83026f61" + "8001" + hex.EncodeToString([]byte{byte(size)})
}

func TestRead(t *testing.T) {
	forms(t, func(t *testing.T, generic bool) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.generic = generic
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
	})
}

// TestReadLargeFile reads 255 octets in chunks that each fit a PoR.
func TestReadLargeFile(t *testing.T) {
	forms(t, func(t *testing.T, generic bool) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.generic = generic
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

		// Every PoR fits the TP-UD of an SMS-DELIVER-REPORT (TS 23.040 clause 9.2.2.1a): the UDH, RPL, RHL and TAR,
		// then CNTR, PCNTR, the status, a CC of 8 octets and the compact response of a chunk, ciphered to the block of
		// triple DES or AES.
		for _, block := range []int{8, 16} {
			secured := 5 + 2 + 8 + 3 + jobs.ReadChunk
			if n := 3 + 6 + secured + (block-secured%block)%block; n > 159 {
				t.Fatalf("PoR of %d octets with blocks of %d", n, block)
			}
		}
	})
}

func TestReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		err    string
	}{
		{"not an FCP", "02" + "9000" + "6f00", "Not an FCP template"},
		{"not entries", fcp(61), "is not a list of 5-octet entries."},
		{"file not found", "01" + "6a82", "Command 1 of 2 failed with status word 6a82."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms(t, func(t *testing.T, generic bool) {
				e := newEnv(t, "+15550000001", sja5(t))
				e.generic = generic
				id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

				e.step()
				e.answer(t, id, ota.StatusPoROK, tc.answer)
				e.step()

				if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, tc.err) {
					t.Fatalf("job = %+v", j)
				}
			})
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
		{"read-back differs", clear2, "03" + "9000" + entry, db.JobFailed, "Slot 2 reads back 999/99 (E-UTRAN WB-S1, E-UTRAN NB-S1, NG-RAN) instead of an empty entry."},
		{"update refused", clear2, "02" + "6982", db.JobFailed, "Command 2 of 3"},
		{"read-back failed", clear2, "03" + "6f00", db.JobUnconfirmed, "Written, but"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms(t, func(t *testing.T, generic bool) {
				e := newEnv(t, "+15550000001", sja5(t))
				e.generic = generic
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
		})
	}
}

// imsiWrite replaces the lab card's IMSI 001010000000001 with 001010000000002, whose EF.IMSI is imsi2.
var imsiWrite = db.Job{Operation: db.OperationIMSIWrite, IMSI: "001010000000002", PreviousIMSI: "001010000000001"}

const imsi2 = "080910100000000020"

// TestIMSIWrite checks that an IMSI write succeeds only when EF.IMSI reads back what it wrote, and that only then
// the card's IMSI on record becomes the new one: a failed write left the card's, and an unconfirmed one may have.
func TestIMSIWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		status db.JobStatus
		err    string
		imsi   string
	}{
		{"written", "03" + "9000" + imsi2, db.JobSucceeded, "", "001010000000002"},
		{"read-back differs", "03" + "9000" + "080910100000000010", db.JobFailed, "EF.IMSI reads back 001010000000001 instead of 001010000000002.", "001010000000001"},
		{"update refused", "02" + "6982", db.JobFailed, "Command 2 of 3", "001010000000001"},
		{"file not found", "01" + "6a82", db.JobFailed, "Command 1 of 3", "001010000000001"},
		{"read-back failed", "03" + "6f00", db.JobUnconfirmed, "Written, but", "001010000000001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, imsiWrite)

			e.step()

			e.answer(t, id, ota.StatusPoROK, tc.answer)
			e.step()

			j := e.job(t, id)
			if j.Status != tc.status || !strings.Contains(j.Error, tc.err) || len(e.smsc.sent()) != 1 {
				t.Fatalf("job = %+v", j)
			}

			card, err := e.store.GetCard(context.Background(), iccid)
			if err != nil || card.IMSI != tc.imsi {
				t.Fatalf("card IMSI = %q, %v, want %s", card.IMSI, err, tc.imsi)
			}
		})
	}

	// Without a PoR, the write may or may not have taken effect: the IMSI on record stays until a late PoR tells.
	t.Run("late PoR", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, imsiWrite)

		e.step()

		j := e.job(t, id)
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))

		card, _ := e.store.GetCard(context.Background(), iccid)
		if j := e.job(t, id); j.Status != db.JobUnconfirmed || card.IMSI != "001010000000001" {
			t.Fatalf("after the receipt, job = %+v, card IMSI %s", j, card.IMSI)
		}

		e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusPoROK, "039000"+imsi2)))

		card, _ = e.store.GetCard(context.Background(), iccid)
		if j := e.job(t, id); j.Status != db.JobSucceeded || card.IMSI != "001010000000002" {
			t.Fatalf("after the PoR, job = %+v, card IMSI %s", j, card.IMSI)
		}
	})

	// A late PoR of a write that a later write followed does not set the IMSI on record: the card ran the later one
	// after it.
	t.Run("late PoR after a later write", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		first := e.queue(t, imsiWrite)

		e.step()

		j := e.job(t, first)
		e.smsc.deliver(t, e.receipt(j, smpp.StateExpired))

		second := e.queue(t, db.Job{Operation: db.OperationIMSIWrite, IMSI: "001010000000003", PreviousIMSI: "001010000000001"})

		e.step()
		e.answer(t, second, ota.StatusPoROK, "039000"+"080910100000000030")
		e.smsc.deliver(t, e.simAnswer(j, e.por(t, first, ota.StatusPoROK, "039000"+imsi2)))

		card, _ := e.store.GetCard(context.Background(), iccid)
		if e.job(t, first).Status != db.JobSucceeded || card.IMSI != "001010000000003" {
			t.Fatalf("first = %+v, card IMSI %s", e.job(t, first), card.IMSI)
		}
	})
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
		{"write answered by SMS-SUBMIT", set, ota.StatusResponseInSubmit, db.JobUnconfirmed, "by SMS-SUBMIT: answer by SMS (0B)."},
		{"write needing more time", set, ota.StatusMoreTime, db.JobUnconfirmed, "more time needed (08)."},
		{"read answered by SMS-SUBMIT", read, ota.StatusResponseInSubmit, db.JobFailed, "answer by SMS (0B)."},
		{"counter low", set, ota.StatusCounterLow, db.JobFailed, "The card refused the command: counter too low (02)."},
		{"TAR unknown", set, ota.StatusTARUnknown, db.JobFailed, "The card has no application at TAR b00001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms(t, func(t *testing.T, generic bool) {
				e := newEnv(t, "+15550000001", sja5(t))
				e.generic = generic
				id := e.queue(t, tc.job)

				e.step()
				e.answer(t, id, tc.status, "")
				e.step()

				if j := e.job(t, id); j.Status != tc.want || !strings.Contains(j.Error, tc.err) || j.PoRStatus != int(tc.status) {
					t.Fatalf("job = %+v", j)
				}
			})
		})
	}
}

// TestPoRNotThisCommand checks that a PoR is attached to a job only when it answers its command: from the card's
// number, with the command's TAR and counter (TS 102 225 clause 5.2, table 3). Another one is acknowledged and left
// aside, and the job keeps waiting for its own (review L1).
func TestPoRNotThisCommand(t *testing.T) {
	forms(t, func(t *testing.T, generic bool) {
		for _, tc := range []struct {
			name    string
			source  string
			tar     []byte
			counter uint64
		}{
			{"other TAR", "15550000001", []byte{0xb0, 0x00, 0x20}, 1},
			{"other counter", "15550000001", tar, 7},
			{"an earlier command's counter", "15550000001", tar, 0},
			{"other card", "15550000002", tar, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newEnv(t, "+15550000001", sja5(t))
				e.generic = generic
				id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

				e.step()

				j := e.job(t, id)
				d := e.simAnswer(j, e.porOf(t, tc.tar, tc.counter, ota.StatusPoROK, "039000ffffff0000"))
				d.Source.Addr = tc.source
				e.smsc.deliver(t, d)

				if j := e.job(t, id); j.Status != db.JobSent || j.Response != nil {
					t.Fatalf("job = %+v", j)
				}

				// Its own PoR is still taken.
				e.answer(t, id, ota.StatusPoROK, "039000ffffff0000")

				if j := e.job(t, id); j.Status != db.JobSucceeded {
					t.Fatalf("job = %+v", j)
				}
			})
		}
	})
}

// TestPoRForms checks that the card's answer is told by its content, a Response Packet behind the user data header
// 02 71 00, from the card's number: as a normal message or an SME Manual/User Acknowledgement, with or without UDHI
// (TS 31.115 clause 4.1), with or without receipted_message_id, in short_message or message_payload (SMPP 3.4
// section 4.6.1).
func TestPoRForms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		esmClass byte
		tlv      bool
		payload  bool
	}{
		{"normal message", 0x00, false, false},
		{"normal message with UDHI", 0x40, false, false},
		{"user acknowledgement without UDHI", 0x10, true, false},
		{"user acknowledgement", 0x50, true, false},
		{"message_payload", 0x40, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "+15550000001", sja5(t))
			id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

			e.step()

			d := e.simAnswer(e.job(t, id), e.por(t, id, ota.StatusPoROK, "039000ffffff0000"))
			d.ESMClass, d.Options = tc.esmClass, smpp.Options{}

			if tc.tlv {
				d.Options.ReceiptedMessageID = new(e.job(t, id).MessageID)
			}

			if tc.payload {
				d.Options.MessagePayload, d.ShortMessage = d.ShortMessage, nil
			}

			e.smsc.deliver(t, d)

			if j := e.job(t, id); j.Status != db.JobSucceeded {
				t.Fatalf("job = %+v", j)
			}
		})
	}

	t.Run("not a PoR", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()

		// A text from the card, an SMSC delivery acknowledgement carrying a PoR, and a PoR behind another header.
		por := e.por(t, id, ota.StatusPoROK, "039000ffffff0000")
		for _, d := range []*smpp.DeliverSM{
			{Message: smpp.Message{Source: smpp.Address{Addr: "15550000001"}, ShortMessage: []byte("hi")}},
			{Message: smpp.Message{Source: smpp.Address{Addr: "15550000001"}, ESMClass: 0x08, ShortMessage: por}},
			{Message: smpp.Message{Source: smpp.Address{Addr: "15550000001"}, ESMClass: 0x40, ShortMessage: append([]byte{5, 0, 3, 1, 1, 1}, por[3:]...)}},
		} {
			e.smsc.deliver(t, d)
		}

		if j := e.job(t, id); j.Status != db.JobSent {
			t.Fatalf("job = %+v", j)
		}
	})
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
		{"write expired", clear2, nil, smpp.StateExpired, db.JobUnconfirmed, "Message expired"},
		{"write undeliverable", clear2, nil, smpp.StateUndeliverable, db.JobUnconfirmed, "Message undeliverable"},
		{"read delivered", read, nil, smpp.StateDelivered, db.JobFailed, "without a PoR"},
		{"read rejected", read, nil, smpp.StateRejected, db.JobFailed, "rejected"},
		// L2: ACCEPTD is final (SMPP 3.4 appendix B, table B-2).
		{"read accepted", read, nil, smpp.StateAccepted, db.JobFailed, "accepted"},
		{"unsecured refusal", clear2, unsecured, smpp.StateDelivered, db.JobFailed, "unsecured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms(t, func(t *testing.T, generic bool) {
				e := newEnv(t, "+15550000001", sja5(t))
				e.generic = generic
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
		})
	}
}

// TestUnsecuredPoRAmbiguous checks that an unsecured PoR, of counter zero, is left aside when two cards of the
// number have a job of the TAR in flight: it cannot tell which it answers.
func TestUnsecuredPoRAmbiguous(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	other := db.Card{
		ICCID: "8949440000001659216", IMSI: "001010000000002", MSISDN: e.msisdn, TAR: tar, KVN: 1, KeySets: []db.KeySet{sja5(t)},
	}

	if err := e.store.CreateCard(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	ids, err := e.store.CreateJobs(context.Background(), []db.Job{{ICCID: other.ICCID, Operation: db.OperationOPLMNRead}})
	if err != nil {
		t.Fatal(err)
	}

	e.step()
	e.smsc.deliver(t, e.simAnswer(e.job(t, id), []byte{2, 0x71, 0, 0, 0x0b, 0x0a, 0xb0, 0, 1, 0, 0, 0, 0, 0, 0, 6}))

	for _, id := range []int64{id, ids[0]} {
		if j := e.job(t, id); j.Status != db.JobSent {
			t.Fatalf("job = %+v", j)
		}
	}
}

// TestReceiptCorrelation checks how a receipt finds its job: by receipted_message_id; without it, by the id of its
// text (SMPP 3.4 appendix B), whose stat then gives the state; and that one for another message is left aside.
func TestReceiptCorrelation(t *testing.T) {
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

	t.Run("receipted_message_id first", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		other := e.job(t, id)
		other.MessageID = "999"
		d := e.receipt(other, smpp.StateDelivered)
		d.Options.ReceiptedMessageID = new(e.job(t, id).MessageID)
		e.smsc.deliver(t, d)

		if j := e.job(t, id); j.Status != db.JobFailed {
			t.Fatalf("job = %+v", j)
		}
	})

	t.Run("receipt for another message", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		j := e.job(t, id)
		j.MessageID = "999"
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))

		if j := e.job(t, id); j.Status != db.JobSent {
			t.Fatalf("job = %+v", j)
		}
	})
}

// TestReceiptBeforePoR checks an SMSC that sends the receipt before the card's answer: the receipt closes the job
// without a PoR, and the PoR, matched by TAR and counter, then gives its outcome.
func TestReceiptBeforePoR(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()

		j := e.job(t, id)
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))

		if j := e.job(t, id); j.Status != db.JobUnconfirmed || !j.PoRMissing {
			t.Fatalf("after the receipt, job = %+v", j)
		}

		e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusPoROK, "039000ffffff0000")))

		if j := e.job(t, id); j.Status != db.JobSucceeded || j.Error != "" || j.PoRMissing {
			t.Fatalf("after the PoR, job = %+v", j)
		}
	})

	// A read closed without the PoR to a step stays failed when the PoR is not its last; the operator runs it again.
	t.Run("read step", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		j := e.job(t, id)
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))
		e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusPoROK, fcp(5))))
		e.step()

		if j := e.job(t, id); j.Status != db.JobFailed || j.FileSize != -1 || len(e.smsc.sent()) != 1 {
			t.Fatalf("job = %+v", j)
		}
	})

	// The PoR of a read's last command still gives its outcome.
	t.Run("read refused", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.step()

		j := e.job(t, id)
		e.smsc.deliver(t, e.receipt(j, smpp.StateDelivered))
		e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusCounterLow, "")))

		if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, "counter too low (02)") || j.PoRMissing {
			t.Fatalf("job = %+v", j)
		}
	})
}

// TestNoAnswer checks that a job sent long past its validity, without a PoR or a receipt, ends; and that its late PoR
// still gives its outcome (review M1).
func TestNoAnswer(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()

	e.now = e.now.Add(24*time.Hour + 5*time.Hour)
	e.step()

	if j := e.job(t, id); j.Status != db.JobSent {
		t.Fatalf("within the grace, job = %+v", j)
	}

	e.now = e.now.Add(2 * time.Hour)
	e.step()

	j := e.job(t, id)
	if j.Status != db.JobUnconfirmed || !strings.Contains(j.Error, "No PoR and no delivery receipt") || !j.PoRMissing {
		t.Fatalf("job = %+v", j)
	}

	e.smsc.deliver(t, e.simAnswer(j, e.por(t, id, ota.StatusPoROK, "039000ffffff0000")))

	if j := e.job(t, id); j.Status != db.JobSucceeded || j.Error != "" {
		t.Fatalf("after the late PoR, job = %+v", j)
	}
}

// TestNoTimeoutWhileUnbound checks that sent jobs do not time out while unbound, nor until the SMSC has had time to
// send what it held once bound again (review M1).
func TestNoTimeoutWhileUnbound(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()

	if err := e.gw.Apply(nil); err != nil {
		t.Fatal(err)
	}

	e.now = e.now.Add(3 * 24 * time.Hour)
	e.step()

	if j := e.job(t, id); j.Status != db.JobSent {
		t.Fatalf("while unbound, job = %+v", j)
	}

	e.rebind(t)

	e.now = e.now.Add(time.Minute)
	e.step()
	e.now = e.now.Add(time.Hour)
	e.step()

	if j := e.job(t, id); j.Status != db.JobSent {
		t.Fatalf("just after the bind, job = %+v", j)
	}

	e.now = e.now.Add(6 * time.Hour)
	e.step()

	if j := e.job(t, id); j.Status != db.JobUnconfirmed {
		t.Fatalf("job = %+v", j)
	}
}

// TestStoreFailures checks that a deliver_sm whose outcome cannot be stored is answered ESME_RX_T_APPN, so that the
// SMSC sends it again, and that the copy then sent is recorded (review H1, M2).
func TestStoreFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(s *faultyStore, err error)
	}{
		{"finish", func(s *faultyStore, err error) { s.finish = err }},
		{"card", func(s *faultyStore, err error) { s.card = err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms(t, func(t *testing.T, generic bool) {
				e := newEnv(t, "+15550000001", sja5(t))
				e.generic = generic
				id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

				e.step()

				d := e.simAnswer(e.job(t, id), e.por(t, id, ota.StatusPoROK, "039000ffffff0000"))

				e.faults.set(func(s *faultyStore) { tc.fault(s, errDB) })

				var se *smpp.StatusError
				if err := e.smsc.try(d); !errors.As(err, &se) || se.Status != smpp.StatusXTAppn {
					t.Fatalf("deliver_sm_resp = %v, want ESME_RX_T_APPN", err)
				}

				if j := e.job(t, id); j.Status != db.JobSent {
					t.Fatalf("after the failure, job = %+v", j)
				}

				e.faults.set(func(s *faultyStore) { tc.fault(s, nil) })
				e.smsc.deliver(t, d)

				if j := e.job(t, id); j.Status != db.JobSucceeded {
					t.Fatalf("after the retry, job = %+v", j)
				}

				// A copy for the closed job is acknowledged.
				e.smsc.deliver(t, d)
			})
		})
	}

	t.Run("receipt", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()

		d := e.receipt(e.job(t, id), smpp.StateExpired)

		e.faults.set(func(s *faultyStore) { s.finish = errDB })

		if err := e.smsc.try(d); err == nil {
			t.Fatal("deliver_sm_resp ESME_ROK, want ESME_RX_T_APPN")
		}

		e.faults.set(func(s *faultyStore) { s.finish = nil })
		e.smsc.deliver(t, d)

		if j := e.job(t, id); j.Status != db.JobUnconfirmed {
			t.Fatalf("job = %+v", j)
		}
	})
}

// TestMarkSentFails checks that a message that cannot be recorded is sent again with a higher counter, and that the
// first one's PoR, of the spent counter, is not taken for the job's (review L6).
func TestMarkSentFails(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.faults.set(func(s *faultyStore) { s.markSent = errDB })
	e.step()
	e.faults.set(func(s *faultyStore) { s.markSent = nil })

	if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 1 {
		t.Fatalf("job = %+v", j)
	}

	e.step()

	j := e.job(t, id)
	if j.Status != db.JobSent || j.Counter != 2 || len(e.smsc.sent()) != 2 {
		t.Fatalf("job = %+v", j)
	}

	e.smsc.deliver(t, e.simAnswer(j, e.porOf(t, tar, 1, ota.StatusPoROK, "039000ffffff0000")))

	if j := e.job(t, id); j.Status != db.JobSent {
		t.Fatalf("after the first message's PoR, job = %+v", j)
	}

	e.answer(t, id, ota.StatusPoROK, "039000ffffff0000")

	if j := e.job(t, id); j.Status != db.JobSucceeded {
		t.Fatalf("job = %+v", j)
	}
}

// TestAES runs a job on an AES key set with 4-octet checksums, which needs the counter (TS 102 225 clause 5.1.2): the
// command's KIc and KID tell AES and the key set, and its CHL the checksum length; the PoR, of counter 1, is the one
// package ota's tests code for the key set of pySim's AES tests.
func TestAES(t *testing.T) {
	por := mustHex(t, "02710000240eb000019fa071ecd80a05e3550a7054feb488f5663864ee046ddd165b39ca3b3f118856")

	forms(t, func(t *testing.T, generic bool) {
		e := newEnv(t, "+15550000001", db.KeySet{
			Version: 2, Algorithm: "aes", KIc: mustHex(t, "200102030405060708090a0b0c0d0e0f"),
			KID: mustHex(t, "201102030405060708090a0b0c0d0e0f"), CCLength: 4,
		})
		e.generic = generic
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

		e.step()

		j := e.job(t, id)
		if c := j.Command; c[5] != 13+4 || c[6] != 0x16 || c[7] != 0x19 || c[8] != 0x22 || c[9] != 0x22 || j.Counter != 1 {
			t.Fatalf("command %x", c)
		}

		e.smsc.deliver(t, e.simAnswer(j, por))
		e.step()

		if j := e.job(t, id); j.Status != db.JobSucceeded {
			t.Fatalf("job = %+v", j)
		}
	})
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

		// The runner backs off: nothing is sent before the backoff ends (review L5).
		e.smsc.setStatus(smpp.StatusOK)
		e.step()

		if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 0 {
			t.Fatalf("during the backoff, job = %+v", j)
		}

		// The retry takes a new counter: the spent one may have reached the card.
		e.now = e.now.Add(time.Minute)
		e.step()

		if j := e.job(t, id); j.Status != db.JobSent || j.Counter != 2 {
			t.Fatalf("after the SMSC came back, job = %+v", j)
		}
	})

	// A throttling stops the batch, and a command that the SMSC never takes fails after the last attempt (review L5).
	t.Run("SMSC keeps throttling", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		other := db.Card{ICCID: "8949440000001659216", IMSI: "001010000000002", MSISDN: "+15550000002", TAR: tar, KVN: 1, KeySets: []db.KeySet{sja5(t)}}

		if err := e.store.CreateCard(context.Background(), other); err != nil {
			t.Fatal(err)
		}

		e.smsc.setStatus(smpp.StatusMsgQFul)
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		ids, err := e.store.CreateJobs(context.Background(), []db.Job{{ICCID: other.ICCID, Operation: db.OperationOPLMNRead}})
		if err != nil {
			t.Fatal(err)
		}

		for range 9 {
			e.step()
			e.now = e.now.Add(10 * time.Minute)
		}

		if j := e.job(t, id); j.Status != db.JobQueued {
			t.Fatalf("after 9 attempts, job = %+v", j)
		}

		if n, _ := e.store.NextCounter(context.Background(), other.ICCID, 1); n != 1 {
			t.Fatalf("the batch went on to the next job, counter %d", n)
		}

		// The attempts are counted on the job: a restarted runner fails it at the tenth.
		restarted := jobs.NewRunner(e.faults, e.runner.Connect, time.Hour, slog.New(slog.DiscardHandler))
		restarted.Now = e.runner.Now
		restarted.Step(context.Background())

		if j := e.job(t, id); j.Status != db.JobFailed || !strings.Contains(j.Error, "10 attempts") {
			t.Fatalf("job = %+v", j)
		}

		if j := e.job(t, ids[0]); j.Status != db.JobQueued {
			t.Fatalf("other job = %+v", j)
		}
	})

	// The SMSC answers ESME_RINVBNDSTS while it unbinds: the job waits for the next bind (review L4).
	t.Run("SMSC unbinding", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		e.smsc.setStatus(smpp.StatusInvBndSts)
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
		e.step()

		if j := e.job(t, id); j.Status != db.JobQueued {
			t.Fatalf("job = %+v", j)
		}

		e.smsc.setStatus(smpp.StatusOK)
		e.step()

		if j := e.job(t, id); j.Status != db.JobSent {
			t.Fatalf("job = %+v", j)
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
		{&smpp.StatusError{Command: smpp.CmdSubmitSMResp, Status: smpp.StatusInvBndSts}, false},
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
		{25*time.Hour + 61*time.Second, "000001010101000R"},
		// L3: the fields stay normalised up to 99 days.
		{jobs.MaxValidity - time.Second, "000098235959000R"},
		{jobs.MaxValidity, "000099000000000R"},
	} {
		got, err := jobs.ValidityPeriod(tc.validity)
		if err != nil || got != tc.want {
			t.Errorf("ValidityPeriod(%v) = %q, %v, want %q", tc.validity, got, err, tc.want)
			continue
		}

		// The SMSC reads it back to the same expiry.
		vp, err := smpp.ParseTime(got, now)
		if err != nil || !vp.At(now).Equal(now.Add(tc.validity)) {
			t.Errorf("%q reads as %v, %v", got, vp.At(now), err)
		}
	}

	for _, v := range []time.Duration{0, jobs.MaxValidity + time.Second, 100 * 24 * time.Hour} {
		if got, err := jobs.ValidityPeriod(v); err == nil {
			t.Errorf("ValidityPeriod(%v) = %q, want an error", v, got)
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
		// The script that the lab sysmoISIM-SJA5 ran over the air: the whole of EF.IMSI, written and read back.
		{imsiWrite, "00a4000c026f07" + "00d6000009" + imsi2 + "00b0000009"},
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
		{Operation: db.OperationIMSIWrite, IMSI: "00101"},
		{Operation: db.OperationIMSIWrite, IMSI: "0010100000000012"},
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
