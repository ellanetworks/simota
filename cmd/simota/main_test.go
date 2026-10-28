package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/ota"
	"github.com/ellanetworks/simota/internal/smsc"
)

// TestSend runs a script against a fake SMSC that answers with the PoR of a sysmoISIM-SJA5 to the same command.
func TestSend(t *testing.T) {
	const (
		command = "00281506191515b00010cf71695f686b9516cfee5d0194c605ac9e8baa3b77f0144614068eb3ab557f93"
		por     = "002c12b0001023dd41b55fb14f5e7726736aeb0bd69e4743f58b520e4f3bc42ca3f66b789b404fb60c953eff1625"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		b, _ := io.ReadAll(r.Body)

		var req struct {
			To     string
			Binary struct {
				ProtocolIdentifier int    `json:"protocol_identifier"`
				DataCodingScheme   int    `json:"data_coding_scheme"`
				UserDataHeader     string `json:"user_data_header"`
				UserData           string `json:"user_data"`
			}
		}

		if err := json.Unmarshal(b, &req); err != nil || req.Binary.ProtocolIdentifier != 0x7f ||
			req.Binary.DataCodingScheme != 0xf6 || req.Binary.UserDataHeader != "027000" || req.Binary.UserData != command {
			t.Errorf("submitted %s", b)
		}

		_, _ = io.WriteString(w, `{"result":{"items":[{"id":7,"status":"pending"}]}}`)
	})
	mux.HandleFunc("GET /api/v1/messages/7", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"id":7,"status":"delivered"}}`)
	})
	mux.HandleFunc("GET /api/v1/messages/7/content", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"deliver_reports":[{"attempt_id":1,"user_data_header":"027100","user_data":"`+por+`"}]}}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	kic, _ := hex.DecodeString("2184A42A93F563DB248095A5F539D489")
	kid, _ := hex.DecodeString("E45C662B528BB14A67F99FE70AB193C9")
	script, _ := hex.DecodeString("a0a40000027f20a0c0000016")

	cmd := ota.Command{
		Keyset: ota.Keyset{Version: 1, Algorithm: ota.TripleDES, KIc: kic, KID: kid},
		SPI: ota.SPI{
			Integrity: ota.IntegrityCC, Ciphering: true,
			PoR: ota.PoRAlways, PoRIntegrity: ota.IntegrityCC, PoRCiphering: true,
		},
		TAR:  [3]byte{0xb0, 0x00, 0x10},
		Data: script,
	}

	c := &smsc.Client{BaseURL: srv.URL, Token: "token"}
	if err := send(context.Background(), io.Discard, c, cmd, "+15550001000", "+15551230001", false); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestBuildScript(t *testing.T) {
	cases := []struct {
		args   []string
		access string
		want   string
	}{
		{[]string{"oplmn", "read"}, "", "a0a40000027f20a0a40000026f61a0b0000000"},
		{[]string{"oplmn", "set", "2", "999", "99"}, "eutran,ngran", "a0a40000027f20a0a40000026f61a0d600050599f9994800a0b0000000"},
		{[]string{"oplmn", "set", "12", "001", "001"}, "gsm", "a0a40000027f20a0a40000026f61a0d60037050011000080a0b0000000"},
		{[]string{"oplmn", "clear", "2"}, "", "a0a40000027f20a0a40000026f61a0d6000505ffffff0000a0b0000000"},
		{[]string{"script", "A0A40000027F20"}, "", "a0a40000027f20"},
	}

	for _, tc := range cases {
		got, err := buildScript(tc.args, tc.access)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}

		if hex.EncodeToString(got) != tc.want {
			t.Fatalf("%v = %x, want %s", tc.args, got, tc.want)
		}
	}
}

func TestBuildScriptRejects(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"oplmn"},
		{"oplmn", "write"},
		{"oplmn", "set", "0", "999", "99"},
		{"oplmn", "set", "52", "999", "99"},
		{"oplmn", "set", "2", "99", "99"},
		{"oplmn", "set", "2", "999", "9"},
		{"oplmn", "clear", "x"},
		{"script", "zz"},
		{"script"},
	} {
		if _, err := buildScript(args, "eutran"); err == nil {
			t.Fatalf("%v: expected an error", args)
		}
	}

	if _, err := buildScript([]string{"oplmn", "set", "2", "999", "99"}, "eutran,lte"); err == nil {
		t.Fatal("expected an error for an unknown access technology")
	}
}

func TestCardSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "simota.db")

	store, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	c := db.Card{
		ICCID: "8949440000001659208", IMSI: "001010000000001", MSISDN: "+15550000001", TAR: []byte{0xb0, 0x00, 0x10}, KVN: 2,
		KeySets: []db.KeySet{
			{Version: 1, Algorithm: "3des", KIc: make([]byte, 16), KID: make([]byte, 16)},
			{Version: 2, Algorithm: "aes", KIc: make([]byte, 16), KID: make([]byte, 16)},
		},
	}
	if err := store.CreateCard(ctx, c); err != nil {
		t.Fatal(err)
	}

	_ = store.Close()

	got, k, err := cardSettings(ctx, path, c.ICCID)
	if err != nil {
		t.Fatal(err)
	}

	if got.MSISDN != c.MSISDN || k.Version != 2 || k.Algorithm != "aes" {
		t.Fatalf("card %+v, key set %+v", got, k)
	}

	if _, _, err := cardSettings(ctx, path, "8988211000000000017"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing card = %v, want ErrNotFound", err)
	}
}

// TestSendFailures checks that send reports a delivery without PoR, a PoR with an error status, and a script whose
// last command failed.
func TestSendFailures(t *testing.T) {
	kic, _ := hex.DecodeString("2184A42A93F563DB248095A5F539D489")
	kid, _ := hex.DecodeString("E45C662B528BB14A67F99FE70AB193C9")
	keyset := ota.Keyset{Version: 1, Algorithm: ota.TripleDES, KIc: kic, KID: kid}
	spi := ota.SPI{Integrity: ota.IntegrityCC, Ciphering: true, PoR: ota.PoRAlways, PoRIntegrity: ota.IntegrityCC, PoRCiphering: true}

	// A PoR of the script's answer: n commands executed, then the status word.
	por := func(t *testing.T, data string) string {
		t.Helper()
		return encodePoR(t, keyset, data)
	}

	cases := []struct {
		name    string
		reports string
		want    string
	}{
		{"no report", `[]`, "no PoR"},
		{"report without user data", `[{"attempt_id":1}]`, "no PoR"},
		{"unsecured error", `[{"attempt_id":1,"user_data":"027100000b0ab0001000000000000006"}]`, "unidentified_security_error"},
		{"failed command", `[{"attempt_id":1,"user_data":"` + por(t, "026a82") + `"}]`, "status word 6a82"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/messages", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"result":{"items":[{"id":7,"status":"pending"}]}}`)
			})
			mux.HandleFunc("GET /api/v1/messages/7", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"result":{"id":7,"status":"delivered"}}`)
			})
			mux.HandleFunc("GET /api/v1/messages/7/content", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"result":{"deliver_reports":`+tc.reports+`}}`)
			})

			srv := httptest.NewServer(mux)
			defer srv.Close()

			cmd := ota.Command{Keyset: keyset, SPI: spi, TAR: [3]byte{0xb0, 0x00, 0x10}, Data: []byte{0xa0, 0xa4, 0x00, 0x00, 0x02, 0x7f, 0x20}}

			err := send(context.Background(), io.Discard, &smsc.Client{BaseURL: srv.URL, Token: "token"}, cmd, "+15550001000", "+15550000001", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// encodePoR is the hex user data, without TP-UDHI as an A56 returns it, of a PoR whose additional data is data.
func encodePoR(t *testing.T, k ota.Keyset, data string) string {
	t.Helper()

	d, _ := hex.DecodeString(data)
	spi := ota.SPI{PoR: ota.PoRAlways, PoRIntegrity: ota.IntegrityCC, PoRCiphering: true}

	udh, ud, err := ota.EncodeResponse(k, spi, ota.Response{TAR: [3]byte{0xb0, 0x00, 0x10}, Data: d})
	if err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(append(udh, ud...))
}
