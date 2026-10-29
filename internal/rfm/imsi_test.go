package rfm

import (
	"encoding/hex"
	"testing"
)

func TestIMSI(t *testing.T) {
	for _, tc := range []struct {
		imsi, ef string
	}{
		// The lab sysmoISIM-SJA5's, read over the air: 15 digits, odd.
		{"001010000000001", "080910100000000010"},
		{"00101000000001", "08011010000000" + "00f1"},
		{"999990123456789", "089999991032547698"},
		{"001010", "04011010f0ffffffff"},
	} {
		ef, err := EncodeIMSI(tc.imsi)
		if err != nil {
			t.Fatalf("%s: %v", tc.imsi, err)
		}

		if got := hex.EncodeToString(ef); got != tc.ef {
			t.Fatalf("EncodeIMSI(%s) = %s, want %s", tc.imsi, got, tc.ef)
		}

		imsi, err := DecodeIMSI(ef)
		if err != nil || imsi != tc.imsi {
			t.Fatalf("DecodeIMSI(%s) = %q, %v, want %s", tc.ef, imsi, err, tc.imsi)
		}
	}

	for _, imsi := range []string{"", "00101", "0010100000000011", "00101000000000a", " 01010000000001"} {
		if ef, err := EncodeIMSI(imsi); err == nil {
			t.Fatalf("EncodeIMSI(%q) = %x, want an error", imsi, ef)
		}
	}

	for name, ef := range map[string]string{
		"erased":         "ffffffffffffffffff",
		"short":          "0809101000000000",
		"length 9":       "090910100000000010",
		"length 0":       "000910100000000010",
		"not an IMSI":    "080a10100000000010",
		"odd but even":   "0809101000000000f1",
		"even but odd":   "080110100000000010",
		"filler inside":  "0809f0100000000010",
		"too few digits": "03091010ffffffffff",
	} {
		if imsi, err := DecodeIMSI(mustHex(t, ef)); err == nil {
			t.Fatalf("%s: DecodeIMSI(%s) = %s, want an error", name, ef, imsi)
		}
	}
}
