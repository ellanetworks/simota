package cardfile

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// sysmocom has the columns of a sysmoISIM-SJA5 key file, with made-up values.
const sysmocom = "IMSI\tICCID\tACC\tPIN1\tPUK1\tPIN2\tPUK2\tKi\tOPC\tADM1\tKIC1\tKID1\tKIK1\tKIC2\tKID2\tKIK2\tKIC3\tKID3\tKIK3\n" +
	"999700000000001\t8988211000000000017\t0001\t0000\t11111111\t0000\t22222222\t000102030405060708090A0B0C0D0E0F\t" +
	"0F0E0D0C0B0A09080706050403020100\t12345678\t11111111111111111111111111111111\t22222222222222222222222222222222\t" +
	"33333333333333333333333333333333\t44444444444444444444444444444444\t55555555555555555555555555555555\t" +
	"66666666666666666666666666666666\t77777777777777777777777777777777\t88888888888888888888888888888888\t\n"

func TestParseSysmocom(t *testing.T) {
	cards, err := Parse(strings.NewReader(sysmocom), "3des")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(cards) != 1 {
		t.Fatalf("%d cards", len(cards))
	}

	c := cards[0]
	if c.ICCID != "8988211000000000017" || c.IMSI != "999700000000001" || len(c.KeySets) != 3 {
		t.Fatalf("card = %+v", c)
	}

	k := c.KeySets[0]
	if k.Version != 1 || hex.EncodeToString(k.KIc) != strings.Repeat("11", 16) || hex.EncodeToString(k.KID) != strings.Repeat("22", 16) {
		t.Fatalf("key set 1 = %+v", k)
	}

	// The same 16-octet keys are AES-128 keys when the card's key sets are AES.
	if cards, err := Parse(strings.NewReader(sysmocom), "aes"); err != nil || len(cards[0].KeySets) != 3 {
		t.Fatalf("Parse as AES = %+v, %v", cards, err)
	}
}

func TestParseSkipsIncompleteKeySet(t *testing.T) {
	file := strings.Replace(sysmocom, "\t88888888888888888888888888888888\t\n", "\t\t\n", 1)

	cards, err := Parse(strings.NewReader(file), "3des")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(cards[0].KeySets) != 2 {
		t.Fatalf("key sets = %+v, want 1 and 2 without 3, which has no KID3", cards[0].KeySets)
	}
}

func TestParseICCIDPadding(t *testing.T) {
	cards, err := Parse(strings.NewReader("ICCID,IMSI,KIC1,KID1\n894944000000165920F,001010000000001,"+
		strings.Repeat("11", 16)+","+strings.Repeat("22", 16)+"\n"), "3des")
	if err != nil || cards[0].ICCID != "894944000000165920" {
		t.Fatalf("cards = %+v, %v", cards, err)
	}

	for in, want := range map[string]string{"8949440000001659208": "8949440000001659208", "894944000000165920f": "894944000000165920"} {
		if got := NormalICCID(in); got != want {
			t.Fatalf("NormalICCID(%s) = %s", in, got)
		}
	}
}

func TestParseRejects(t *testing.T) {
	k16 := strings.Repeat("11", 16)

	for _, file := range []string{
		"",
		"ICCID,IMSI\n",
		"ICCID,KIC1\n8988211000000000017,00\n",
		"ICCID,IMSI,KIC1\n8988211000000000017,001010000000001," + k16 + "\n",
		"ICCID,IMSI\n12,001010000000001\n",
		"ICCID,IMSI,KIC1,KID1\n8988211000000000017,001010000000001,zz,00\n",
		"ICCID,IMSI,KIC1,KID1\n8988211000000000017,001010000000001,0011,0011\n",
		"ICCID,IMSI,KIC1,KID1\n8988211000000000017,001010000000001," + k16 + "," + strings.Repeat("22", 24) + "\n",
		"ICCID,IMSI,KIC1,KID1\n8988211000000000017,001010000000001," + strings.Repeat("11", 24) + "," + strings.Repeat("22", 24) + "\n",
	} {
		if _, err := Parse(strings.NewReader(file), "3des"); !errors.Is(err, ErrFormat) {
			t.Fatalf("Parse(%q) = %v, want ErrFormat", file, err)
		}
	}

	if _, err := Parse(strings.NewReader(sysmocom), "des"); err == nil {
		t.Fatal("expected an error for an unknown algorithm")
	}
}
