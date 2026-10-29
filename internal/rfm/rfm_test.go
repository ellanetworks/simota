package rfm

import (
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestScript(t *testing.T) {
	entry, err := SelectorEntry{MCC: "999", MNC: "99", Access: 0x4800}.Encode()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		script *Script
		want   string
	}{
		{"UICC select for the FCP", NewScript().SelectFCP(FileOPLMNwAcT).GetResponse(), "00a40004026f6100c0000000"},
		{
			"UICC update slot 51, read it back",
			NewScript().Select(FileOPLMNwAcT).UpdateBinary(SlotOffset(51), entry).ReadBinary(SlotOffset(51), 5),
			"00a4000c026f6100d600fa0599f999480000b000fa05",
		},
		{"UICC read a chunk", NewScript().Select(FileOPLMNwAcT).ReadBinary(120, 120), "00a4000c026f6100b0007878"},
	}

	for _, tc := range cases {
		b, err := tc.script.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if got := hex.EncodeToString(b); got != tc.want {
			t.Fatalf("%s = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestScriptRejects(t *testing.T) {
	for name, s := range map[string]*Script{
		"empty":                   NewScript(),
		"command after read":      NewScript().ReadBinary(0, 5).Select(FileOPLMNwAcT),
		"read without length":     NewScript().ReadBinary(0, 0),
		"read offset as SFI":      NewScript().ReadBinary(0x8000, 5),
		"update offset as SFI":    NewScript().UpdateBinary(0x8000, []byte{1}),
		"empty update":            NewScript().UpdateBinary(0, nil),
		"get response alone":      NewScript().GetResponse(),
		"get response after read": NewScript().Select(FileOPLMNwAcT).UpdateBinary(0, []byte{1}).GetResponse(),
		"update longer than a P3": NewScript().UpdateBinary(0, make([]byte, 256)),
	} {
		if _, err := s.Bytes(); !errors.Is(err, ErrScript) {
			t.Fatalf("%s: err = %v, want ErrScript", name, err)
		}
	}
}

func TestCheck(t *testing.T) {
	read := NewScript().Select(FileOPLMNwAcT).ReadBinary(0, 5)
	update := NewScript().Select(FileOPLMNwAcT).UpdateBinary(0, []byte{1})
	fcp := NewScript().SelectFCP(FileOPLMNwAcT).GetResponse()

	cases := []struct {
		name    string
		script  *Script
		resp    string
		ok      bool
		warning bool
	}{
		{"read", read, "02900000f110ffff", true, false},
		{"read ending on '62 82'", read, "02628200f110", true, true},
		{"read without data", read, "029000", false, false},
		{"read stopped at select", read, "016a82", false, false},
		{"read ending on '61 XX'", read, "02611000f110ffff", false, false},
		{"read ending on '9F XX'", read, "029f1000f110ffff", false, false},
		{"update", update, "029000", true, false},
		{"update with a proactive command", update, "02910f", true, false},
		{"update after retries", update, "0263c1", true, true},
		{"update refused", update, "026982", false, false},
		{"FCP", fcp, "02900062038002003c", true, false},
		{"update ending on '92 01'", update, "029201", false, false},
	}

	for _, tc := range cases {
		r, err := DecodeResponse(mustHex(t, tc.resp))
		if err != nil {
			t.Fatal(err)
		}

		res, err := tc.script.Check(r)
		if (err == nil) != tc.ok || res.Warning != tc.warning {
			t.Fatalf("%s: result %+v, err %v", tc.name, res, err)
		}

		var se *ScriptError
		if err != nil && strings.Contains(tc.name, "stopped") && (!errors.As(err, &se) || se.Executed != 1 || se.Commands != 2) {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
	}
}

func TestFileSize(t *testing.T) {
	// An FCP template with a file descriptor, a file identifier, and a file size of 60 octets.
	size, err := FileSize(mustHex(t, "620b"+"82024121"+"83026f61"+"8001"+"3c"))
	if err != nil || size != 60 {
		t.Fatalf("size = %d, %v", size, err)
	}

	size, err = FileSize(mustHex(t, "6204"+"8002"+"00ff"))
	if err != nil || size != 255 {
		t.Fatalf("size = %d, %v", size, err)
	}

	for _, fcp := range []string{"", "6f0280", "62048202412", "6203820241", "620482024121", "62038005"} {
		b, _ := hex.DecodeString(fcp)
		if _, err := FileSize(b); err == nil {
			t.Fatalf("FileSize(%s): expected an error", fcp)
		}
	}
}

func TestSelector(t *testing.T) {
	b := mustHex(t, "00f110ffff99f9994800ffffff0000"+"216354"+"6000")

	got, err := DecodeSelector(b)
	if err != nil {
		t.Fatal(err)
	}

	want := []SelectorEntry{
		{MCC: "001", MNC: "01", Access: 0xffff},
		{MCC: "999", MNC: "99", Access: 0x4800},
		Empty,
		{MCC: "123", MNC: "456", Access: 0x6000},
	}

	if !slices.Equal(got, want) {
		t.Fatalf("entries = %+v", got)
	}

	for i, e := range want {
		enc, err := e.Encode()
		if err != nil {
			t.Fatal(err)
		}

		if e != Empty && hex.EncodeToString(enc) != hex.EncodeToString(b[i*5:i*5+5]) {
			t.Fatalf("entry %d encodes as %x", i+1, enc)
		}
	}
}

// TestAccess checks the access technology bits and the qualifiers of TS 31.102 tables 4.2.5.1 and 4.2.5.2.
func TestAccess(t *testing.T) {
	cases := []struct {
		access AccessTechnology
		names  string
	}{
		{0x4000, "eutran_wb_s1 eutran_nb_s1"},
		{0x7000, "eutran_wb_s1 eutran_nb_s1"},
		{0x6000, "eutran_wb_s1"},
		{0x5000, "eutran_nb_s1"},
		{0x2000, ""},
		{0x4800, "eutran_wb_s1 eutran_nb_s1 ngran"},
		{0x0080, "gsm ec_gsm_iot"},
		{0x008c, "gsm ec_gsm_iot"},
		{0x0084, "gsm"},
		{0x0088, "ec_gsm_iot"},
		{0x0008, ""},
		{0x8000, "utran"},
		{0x0700, "satellite_ngran satellite_eutran_wb_s1 satellite_eutran_nb_s1"},
		{0x0070, "gsm_compact cdma2000_hrpd cdma2000_1xrtt"},
		{0xffff, "utran eutran_wb_s1 eutran_nb_s1 ngran satellite_ngran satellite_eutran_wb_s1 satellite_eutran_nb_s1 gsm ec_gsm_iot gsm_compact cdma2000_hrpd cdma2000_1xrtt"},
	}

	for _, tc := range cases {
		if got := strings.Join(tc.access.Names(), " "); got != tc.names {
			t.Fatalf("%#04x = %q, want %q", uint16(tc.access), got, tc.names)
		}
	}

	for _, tc := range []struct {
		names  []string
		access AccessTechnology
	}{
		{[]string{EUTRANWBS1, EUTRANNBS1, NGRAN}, 0x4800},
		{[]string{EUTRANWBS1}, 0x6000},
		{[]string{EUTRANNBS1}, 0x5000},
		{[]string{GSM, ECGSMIoT}, 0x0080},
		{[]string{GSM}, 0x0084},
		{[]string{ECGSMIoT}, 0x0088},
		{[]string{UTRAN, SatelliteNGRAN, CDMA20001xRTT}, 0x8410},
	} {
		got, err := ParseAccess(tc.names)
		if err != nil || got != tc.access {
			t.Fatalf("ParseAccess(%v) = %#04x, %v, want %#04x", tc.names, uint16(got), err, uint16(tc.access))
		}

		// Decoding gives the names back.
		if back := got.Names(); !slices.Equal(back, sortedLike(tc.names)) {
			t.Fatalf("%#04x decodes as %v", uint16(got), back)
		}
	}

	for _, names := range [][]string{nil, {"eutran"}, {"lte"}} {
		if _, err := ParseAccess(names); err == nil {
			t.Fatalf("ParseAccess(%v): expected an error", names)
		}
	}
}

func sortedLike(names []string) []string {
	var out []string

	for _, n := range AccessNames {
		if slices.Contains(names, n) {
			out = append(out, n)
		}
	}

	return out
}
