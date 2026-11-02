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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
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

func sja5(t *testing.T) db.KeySet {
	return db.KeySet{
		Version: 1, Algorithm: "3des", KIc: mustHex(t, "2184A42A93F563DB248095A5F539D489"),
		KID: mustHex(t, "E45C662B528BB14A67F99FE70AB193C9"),
	}
}

// ETSI TS 102 225 clause 5.2
// TS 31.115 clause 4.4
func porPacket(t *testing.T, k ota.Keyset, r ota.Response) []byte {
	t.Helper()

	if k.Algorithm != ota.TripleDES {
		t.Fatal("porPacket codes triple DES PoRs only")
	}

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

	mac := cbc(k.KID, slices.Concat(udh, header, counter, data))

	return slices.Concat(udh, header, cbc(k.KIc, slices.Concat(counter, mac[len(mac)-ccLength:], data)))
}

type fakeSMSC struct {
	srv *smpp.Server

	mu        sync.Mutex
	status    smpp.CommandStatus
	submitted []*smpp.SubmitSM
	onSubmit  func()
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
			onSubmit := f.onSubmit
			f.mu.Unlock()

			if onSubmit != nil {
				onSubmit()
			}

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

func (f *fakeSMSC) deliver(t *testing.T, d *smpp.DeliverSM) {
	t.Helper()

	if err := f.try(d); err != nil {
		t.Fatalf("deliver_sm: %v", err)
	}
}

func (f *fakeSMSC) try(d *smpp.DeliverSM) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.srv.Deliver(ctx, "simota", d)

	return err
}

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
	store    *db.DB
	faults   *faultyStore
	smsc     *fakeSMSC
	gw       *gateway.Gateway
	runner   *jobs.Runner
	keyset   ota.Keyset
	msisdn   string
	now      time.Time
	validity time.Duration
	settings gateway.Settings
	spans    *tracetest.SpanRecorder
	tracer   trace.Tracer
	generic  bool
}

func forms(t *testing.T, run func(t *testing.T, generic bool)) {
	t.Run("ella", func(t *testing.T) { run(t, false) })
	t.Run("generic", func(t *testing.T) { run(t, true) })
}

func newEnv(t *testing.T, msisdn string, keys db.KeySet) *env {
	t.Helper()

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "simota.db"), db.WithTracerProvider(tp))
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
		validity: 24 * time.Hour, spans: spans, tracer: tp.Tracer("test"),
	}

	e.runner = jobs.NewRunner(e.faults, func(context.Context) (jobs.Gateway, error) {
		return jobs.Gateway{SMSC: e.gw, Originator: "+15550001000", Validity: e.validity}, nil
	}, time.Hour, slog.New(slog.DiscardHandler))
	e.runner.Now = func() time.Time { return e.now }
	e.runner.TracerProvider = tp

	e.settings = gateway.Settings{Host: "127.0.0.1", Port: port, SystemID: "simota", Password: "secret"}
	e.gw = gateway.New(gateway.Config{Handler: e.runner, TracerProvider: tp})

	t.Cleanup(func() { _ = e.gw.Close(context.Background()) })

	e.rebind(t)

	return e
}

func (e *env) rebind(t *testing.T) {
	t.Helper()

	if err := e.gw.Apply(context.Background(), new(e.settings)); err != nil {
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

// SMPP 3.4 section 2.11
// SMPP 3.4 appendix B
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

func (e *env) por(t *testing.T, id int64, status ota.Status, data string) []byte {
	t.Helper()

	j := e.job(t, id)

	return e.porOf(t, j.TAR, j.Counter, status, data)
}

func (e *env) porOf(t *testing.T, tar []byte, counter uint64, status ota.Status, data string) []byte {
	t.Helper()

	r := ota.Response{Counter: counter, Status: status, Data: mustHex(t, data)}
	copy(r.TAR[:], tar)

	return porPacket(t, e.keyset, r)
}

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

		b := e.smsc.sent()[0]
		if b.Source != (smpp.Address{TON: 1, NPI: 1, Addr: "15550001000"}) || b.Destination != (smpp.Address{TON: 1, NPI: 1, Addr: "15550000001"}) ||
			b.ESMClass != 0x40 || b.ProtocolID != 0x7f || b.DataCoding != 0xf6 || b.RegisteredDelivery != 0x09 ||
			b.ValidityPeriod != "000001000000000R" || b.ScheduleDeliveryTime != "" || !bytes.HasPrefix(b.ShortMessage, []byte{2, 0x70, 0}) ||
			!bytes.Equal(b.ShortMessage, j.Command) {
			t.Fatalf("submitted %+v", b)
		}

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

var imsiWrite = db.Job{Operation: db.OperationIMSIWrite, IMSI: "001010000000002", PreviousIMSI: "001010000000001"}

const imsi2 = "080910100000000020"

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

// ETSI TS 102 225 clause 5.2 table 3
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

				e.answer(t, id, ota.StatusPoROK, "039000ffffff0000")

				if j := e.job(t, id); j.Status != db.JobSucceeded {
					t.Fatalf("job = %+v", j)
				}
			})
		}
	})
}

// TS 31.115 clause 4.1
// SMPP 3.4 section 4.6.1
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
		answer []byte
		state  smpp.MessageState
		want   db.JobStatus
		err    string
	}{
		{"write delivered", clear2, nil, smpp.StateDelivered, db.JobUnconfirmed, "cannot authenticate"},
		{"write expired", clear2, nil, smpp.StateExpired, db.JobUnconfirmed, "Message expired"},
		{"write undeliverable", clear2, nil, smpp.StateUndeliverable, db.JobUnconfirmed, "Message undeliverable"},
		{"read delivered", read, nil, smpp.StateDelivered, db.JobFailed, "without a PoR"},
		{"read rejected", read, nil, smpp.StateRejected, db.JobFailed, "rejected"},
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

// SMPP 3.4 appendix B
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

func TestNoTimeoutWhileUnbound(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	id := e.queue(t, db.Job{Operation: db.OperationOPLMNClear, Slot: 2})

	e.step()

	if err := e.gw.Apply(context.Background(), nil); err != nil {
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

// ETSI TS 102 225 clause 5.1.2
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

		e.smsc.setStatus(smpp.StatusOK)
		e.step()

		if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 0 {
			t.Fatalf("during the backoff, job = %+v", j)
		}

		e.now = e.now.Add(time.Minute)
		e.step()

		if j := e.job(t, id); j.Status != db.JobSent || j.Counter != 2 {
			t.Fatalf("after the SMSC came back, job = %+v", j)
		}
	})

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

	t.Run("store fails", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

		e.faults.set(func(s *faultyStore) { s.card = errDB })
		e.step()

		if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 0 {
			t.Fatalf("while the store fails, job = %+v", j)
		}

		e.faults.set(func(s *faultyStore) { s.card = nil })
		e.step()

		if j := e.job(t, id); j.Status != db.JobSent || j.Counter != 1 {
			t.Fatalf("once the store answers, job = %+v", j)
		}
	})

	t.Run("not bound", func(t *testing.T) {
		e := newEnv(t, "+15550000001", sja5(t))
		if err := e.gw.Apply(context.Background(), nil); err != nil {
			t.Fatal(err)
		}

		id := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})
		e.step()

		if j := e.job(t, id); j.Status != db.JobQueued || len(e.smsc.sent()) != 0 {
			t.Fatalf("job = %+v", j)
		}

		if n, err := e.store.NextCounter(context.Background(), iccid, 1); err != nil || n != 1 {
			t.Fatalf("next counter = %d, %v", n, err)
		}
	})
}

func TestStop(t *testing.T) {
	e := newEnv(t, "+15550000001", sja5(t))
	logs := &recorder{}
	e.runner.Logger = slog.New(logs)

	other := db.Card{ICCID: "8949440000001659216", IMSI: "001010000000002", MSISDN: "+15550000002", TAR: tar, KVN: 1, KeySets: []db.KeySet{sja5(t)}}
	if err := e.store.CreateCard(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	first := e.queue(t, db.Job{Operation: db.OperationOPLMNRead})

	ids, err := e.store.CreateJobs(context.Background(), []db.Job{{ICCID: other.ICCID, Operation: db.OperationOPLMNRead}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	e.smsc.mu.Lock()
	e.smsc.onSubmit = func() {
		stop()
		time.Sleep(50 * time.Millisecond)
	}
	e.smsc.mu.Unlock()

	e.runner.Step(ctx)

	if j := e.job(t, first); j.Status != db.JobSent || j.MessageID != "1" || j.Counter != 1 {
		t.Fatalf("job whose message the SMSC took = %+v", j)
	}

	if j := e.job(t, ids[0]); j.Status != db.JobQueued || len(e.smsc.sent()) != 1 {
		t.Fatalf("next job = %+v, %d messages sent", j, len(e.smsc.sent()))
	}

	if l := logs.above(slog.LevelWarn); len(l) > 0 {
		t.Fatalf("logged on stop: %v", l)
	}

	if p := e.named(processName); len(p) != 1 || spanAttrs(p[0])["ella.ota.job.outcome"] != "sent" {
		t.Fatalf("process spans on stop: %d", len(p))
	}

	e.runner.Step(ctx)

	if len(e.smsc.sent()) != 1 {
		t.Fatalf("a stopped runner sent %d messages", len(e.smsc.sent()))
	}
}

type recorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()

	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *recorder) WithGroup(string) slog.Handler { return r }

func (r *recorder) above(l slog.Level) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string

	for _, rec := range r.records {
		if rec.Level >= l {
			out = append(out, rec.Message)
		}
	}

	return out
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
		{jobs.MaxValidity - time.Second, "000098235959000R"},
		{jobs.MaxValidity, "000099000000000R"},
	} {
		got, err := jobs.ValidityPeriod(tc.validity)
		if err != nil || got != tc.want {
			t.Errorf("ValidityPeriod(%v) = %q, %v, want %q", tc.validity, got, err, tc.want)
			continue
		}

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
