package rfm

import (
	"encoding/hex"
	"errors"
	"testing"
)

// TestScript checks the scripts that were run on a sysmoISIM-SJA5 through its SIM RFM application.
func TestScript(t *testing.T) {
	entry, err := SelectorEntry{MCC: "999", MNC: "99", Access: EUTRAN | NGRAN}.Encode()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		script *Script
		want   string
	}{
		{"select, get response", NewScript(ClassGSM).Select(FileDFGSM).GetResponse(0x16), "a0a40000027f20a0c0000016"},
		{
			"update slot 2, read back",
			NewScript(ClassGSM).Select(FileDFGSM).Select(FileOPLMNwAcT).UpdateBinary(SlotOffset(2), entry).ReadBinary(0, 15),
			"a0a40000027f20a0a40000026f61a0d600050599f9994800a0b000000f",
		},
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

func TestScriptRejectsCommandAfterRead(t *testing.T) {
	_, err := NewScript(ClassGSM).ReadBinary(0, 0).Select(FileDFGSM).Bytes()
	if !errors.Is(err, ErrScript) {
		t.Fatalf("err = %v, want ErrScript", err)
	}
}

func TestDecodeResponse(t *testing.T) {
	// The compact response puts the status word before the data.
	b, _ := hex.DecodeString("04900000f110ffff99f9994800ffffff0000")

	r, err := DecodeResponse(b)
	if err != nil {
		t.Fatal(err)
	}

	if r.Executed != 4 || r.StatusWord != 0x9000 || !r.OK() || hex.EncodeToString(r.Data) != "00f110ffff99f9994800ffffff0000" {
		t.Fatalf("response = %+v", r)
	}
}

func TestSelector(t *testing.T) {
	b, _ := hex.DecodeString("00f110ffff99f9994800ffffff0000" + "216354" + "4000")

	got, err := DecodeSelector(b)
	if err != nil {
		t.Fatal(err)
	}

	want := []SelectorEntry{
		{MCC: "001", MNC: "01", Access: 0xffff},
		{MCC: "999", MNC: "99", Access: EUTRAN | NGRAN},
		Empty,
		{MCC: "123", MNC: "456", Access: EUTRAN},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d entries", len(got))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i+1, got[i], want[i])
		}

		enc, err := got[i].Encode()
		if err != nil {
			t.Fatal(err)
		}

		if want[i] != Empty && hex.EncodeToString(enc) != hex.EncodeToString(b[i*5:i*5+5]) {
			t.Fatalf("entry %d encodes as %x", i+1, enc)
		}
	}
}
