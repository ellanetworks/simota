package ota

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}

	return b
}

// sja5 is key set 1 of a sysmoISIM-SJA5. The packets of the tests that use it were exchanged with the card.
func sja5(t *testing.T) Keyset {
	return Keyset{
		Version:   1,
		Algorithm: TripleDES,
		KIc:       mustHex(t, "2184A42A93F563DB248095A5F539D489"),
		KID:       mustHex(t, "E45C662B528BB14A67F99FE70AB193C9"),
	}
}

// pysim3DES is key set 3 of the triple DES tests of pySim (tests/unittests/test_ota.py).
func pysim3DES(t *testing.T) Keyset {
	return Keyset{
		Version:   3,
		Algorithm: TripleDES,
		KIc:       mustHex(t, "C21DD66ACAC13CB3BC8B331B24AFB57B"),
		KID:       mustHex(t, "12110C78E678C25408233076AA033615"),
	}
}

// pysimAES is key set 2 of the AES tests of pySim.
func pysimAES(t *testing.T) Keyset {
	return Keyset{
		Version:   2,
		Algorithm: AES,
		KIc:       mustHex(t, "200102030405060708090a0b0c0d0e0f"),
		KID:       mustHex(t, "201102030405060708090a0b0c0d0e0f"),
	}
}

// secured is the SPI of a ciphered command with a CC that asks for a ciphered PoR with a CC.
var secured = SPI{
	Integrity:    IntegrityCC,
	Ciphering:    true,
	PoR:          PoRAlways,
	PoRIntegrity: IntegrityCC,
	PoRCiphering: true,
}

var simRFM = [3]byte{0xb0, 0x00, 0x10}

func TestEncodeCommand(t *testing.T) {
	counted := secured
	counted.Counter = CounterHigher

	cases := []struct {
		name    string
		command Command
		want    string
	}{
		{
			name:    "select DF.GSM, get response",
			command: Command{Keyset: sja5(t), SPI: secured, TAR: simRFM, Data: mustHex(t, "a0a40000027f20a0c0000016")},
			want:    "00281506191515b00010cf71695f686b9516cfee5d0194c605ac9e8baa3b77f0144614068eb3ab557f93",
		},
		{
			name:    "read EF.OPLMNwAcT",
			command: Command{Keyset: sja5(t), SPI: secured, TAR: simRFM, Data: mustHex(t, "a0a40000027f20a0a40000026f61a0b000000f")},
			want:    "00301506191515b000109a46af5dbcd005a46055f55440c44512bd230ba12854b987522f687b50bc495d8638822e5edaef0e",
		},
		{
			name:    "counter",
			command: Command{Keyset: sja5(t), SPI: counted, TAR: simRFM, Counter: 100, Data: mustHex(t, "a0a40000027f20a0c0000016")},
			want:    "00281516191515b000108fb60fad7125e0ee4811b11427719bef3eb8b1ef6ecb1b4f326272f85256243e",
		},
		{
			// pySim sets the unused KID to the key set; it is zero here (TS 102 225 clause 5.1).
			name: "ciphered without CC",
			command: Command{
				Keyset: pysim3DES(t),
				SPI:    SPI{Ciphering: true, PoR: PoRAlways, PoRIntegrity: IntegrityCC, PoRCiphering: true},
				TAR:    [3]byte{0xb0, 0x00, 0x00},
				Data:   mustHex(t, "00a40000023f00"),
			},
			want: "00180d04193500b00000e3ec80a849b554421276af3883927c20",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			udh, ud, err := tc.command.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			if !bytes.Equal(udh, []byte{0x02, 0x70, 0x00}) {
				t.Fatalf("udh = %x", udh)
			}

			if got := hex.EncodeToString(ud); got != tc.want {
				t.Fatalf("ud = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestEncodeCommandAES checks AES ciphering and CMAC against pySim, whose vector has no counter, which TS 102 225
// forbids with AES and Encode refuses.
func TestEncodeCommandAES(t *testing.T) {
	c := Command{Keyset: pysimAES(t), SPI: secured, TAR: [3]byte{0xb0, 0x00, 0x11}, Data: mustHex(t, "00a40004023f00")}

	if _, _, err := c.Encode(); err == nil {
		t.Fatal("expected Encode to refuse AES without a counter")
	}

	spi, _ := c.SPI.encode()

	_, ud, err := c.encode(spi)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if got, want := hex.EncodeToString(ud), "00281506192222b00011e87cceebb2d93083011ce294f93fc4d8de80da1abae8c37ca3e72ec4432e5058"; got != want {
		t.Fatalf("ud = %s, want %s", got, want)
	}
}

func TestEncodeCommandRejects(t *testing.T) {
	short := sja5(t)
	short.KID = short.KID[:8]

	counted := secured
	counted.Counter = CounterHigher

	for name, c := range map[string]Command{
		"key version 0":       {Keyset: Keyset{Algorithm: TripleDES, KIc: sja5(t).KIc, KID: sja5(t).KID}, SPI: secured},
		"short KID":           {Keyset: short, SPI: secured},
		"unknown algorithm":   {Keyset: Keyset{Version: 1, Algorithm: 0x9, KIc: sja5(t).KIc, KID: sja5(t).KID}, SPI: secured},
		"redundancy check":    {Keyset: sja5(t), SPI: SPI{Integrity: IntegrityRC}},
		"counter without SPI": {Keyset: sja5(t), SPI: secured, Counter: 1},
		"counter overflow":    {Keyset: sja5(t), SPI: counted, Counter: 1 << 40},
		"SPI out of range":    {Keyset: sja5(t), SPI: SPI{PoR: 3}},
	} {
		if _, _, err := c.Encode(); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestEncodeCommandTooLong(t *testing.T) {
	c := Command{Keyset: sja5(t), SPI: secured, TAR: simRFM, Data: make([]byte, 120)}
	if _, _, err := c.Encode(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDecodeResponse(t *testing.T) {
	signed := secured
	signed.PoRCiphering = false

	plain := secured
	plain.PoRCiphering = false
	plain.PoRIntegrity = IntegrityNone

	cases := []struct {
		name     string
		keyset   Keyset
		spi      SPI
		packet   string
		tar      string
		status   Status
		data     string
		unsecure bool
	}{
		{
			name:   "SJA5, select DF.GSM",
			keyset: sja5(t),
			spi:    secured,
			packet: "027100002c12b0001023dd41b55fb14f5e7726736aeb0bd69e4743f58b520e4f3bc42ca3f66b789b404fb60c953eff1625",
			tar:    "b00010",
			data:   "0290000000ffff7f2002000000000009b106350400838a838a",
		},
		{
			name:   "3DES ciphered",
			keyset: pysim3DES(t),
			spi:    secured,
			packet: "027100001c12b000119660ebdb81be189b5e4389e9e7ab2bc0954f963ad869ed7c",
			tar:    "b00011",
			data:   "01612f",
		},
		{
			name:   "3DES signed",
			keyset: pysim3DES(t),
			spi:    signed,
			packet: "027100001612b000110000000000000055f47118381175fb01612f",
			tar:    "b00011",
			data:   "01612f",
		},
		{
			name:   "3DES clear",
			keyset: pysim3DES(t),
			spi:    plain,
			packet: "027100000e0ab000110000000000000001612f",
			tar:    "b00011",
			data:   "01612f",
		},
		{
			name:   "AES ciphered",
			keyset: pysimAES(t),
			spi:    secured,
			packet: "027100002412b00011ebc6b497e2cad7aedf36ace0e3a29b38853f0fe9ccde81913be5702b73abce1f",
			tar:    "b00011",
			data:   "016132",
		},
		{
			// From pySim's smpp-ota-tool documentation: a card refusing a command it cannot authenticate.
			name:     "unsecured error",
			keyset:   sja5(t),
			spi:      secured,
			packet:   "027100000b0ab0001000000000000006",
			tar:      "b00010",
			status:   StatusUnidentifiedSecurityError,
			unsecure: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustHex(t, tc.packet)

			r, err := DecodeResponse(tc.keyset, tc.spi, p[:3], p[3:])
			if err != nil {
				t.Fatalf("DecodeResponse: %v", err)
			}

			if hex.EncodeToString(r.TAR[:]) != tc.tar || r.Status != tc.status || hex.EncodeToString(r.Data) != tc.data ||
				r.Unsecured != tc.unsecure {
				t.Fatalf("response = %+v (data %x)", r, r.Data)
			}
		})
	}
}

// TestDecodeResponseWithoutUDHI decodes the PoR of a sysmoISIM-SJA5 that a Samsung Galaxy A56 returned in an
// SMS-DELIVER-REPORT without TP-UDHI, with the Response Packet's UDH at the start of the user data.
func TestDecodeResponseWithoutUDHI(t *testing.T) {
	p := mustHex(t, "027100005412b000109996c2cab4a3302b80c88fe8c5d47462074bd5d39df6ae0016fb515839a2fad763fe44982996c31bc5ff2d1f49356c6800431e439638557e8f43e0ea30aa91057a2667679025c0fcc449ddc873ee0347")

	r, err := DecodeResponse(sja5(t), secured, nil, p)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}

	if r.Status != StatusPoROK || len(r.Data) < 3 {
		t.Fatalf("response = %+v", r)
	}
}

// TestEncodeResponse reproduces the PoRs of the SJA5 and of pySim's tests from their decoded content.
func TestEncodeResponse(t *testing.T) {
	signed := secured
	signed.PoRCiphering = false

	for _, tc := range []struct {
		keyset Keyset
		spi    SPI
		packet string
	}{
		{sja5(t), secured, "027100002c12b0001023dd41b55fb14f5e7726736aeb0bd69e4743f58b520e4f3bc42ca3f66b789b404fb60c953eff1625"},
		{pysim3DES(t), secured, "027100001c12b000119660ebdb81be189b5e4389e9e7ab2bc0954f963ad869ed7c"},
		{pysim3DES(t), signed, "027100001612b000110000000000000055f47118381175fb01612f"},
		{pysimAES(t), secured, "027100002412b00011ebc6b497e2cad7aedf36ace0e3a29b38853f0fe9ccde81913be5702b73abce1f"},
	} {
		p := mustHex(t, tc.packet)

		r, err := DecodeResponse(tc.keyset, tc.spi, p[:3], p[3:])
		if err != nil {
			t.Fatalf("DecodeResponse %s: %v", tc.packet, err)
		}

		udh, ud, err := EncodeResponse(tc.keyset, tc.spi, r)
		if err != nil {
			t.Fatalf("EncodeResponse: %v", err)
		}

		if got := hex.EncodeToString(append(udh, ud...)); got != tc.packet {
			t.Fatalf("EncodeResponse = %s, want %s", got, tc.packet)
		}
	}
}

func TestDecodeResponseRejectsBadChecksum(t *testing.T) {
	signed := secured
	signed.PoRCiphering = false

	p := mustHex(t, "027100001612b000110000000000000055f47118381175fb02612f")
	if _, err := DecodeResponse(pysim3DES(t), signed, p[:3], p[3:]); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("err = %v, want ErrIntegrity", err)
	}
}

func TestDecodeResponseRejectsCommand(t *testing.T) {
	p := mustHex(t, "02700000281506191515b00010cf71695f686b9516cfee5d0194c605ac9e8baa3b77f0144614068eb3ab557f93")
	if _, err := DecodeResponse(sja5(t), secured, p[:3], p[3:]); !errors.Is(err, ErrNotResponse) {
		t.Fatalf("err = %v, want ErrNotResponse", err)
	}
}

// TestCMAC checks the AES-CMAC examples of RFC 4493 clause 4.
func TestCMAC(t *testing.T) {
	k := Keyset{Algorithm: AES, KID: mustHex(t, "2b7e151628aed2a6abf7158809cf4f3c")}
	msg := mustHex(t, "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710")

	cases := []struct {
		n    int
		want string
	}{
		{0, "bb1d6929e95937287fa37d129b756746"},
		{16, "070a16b46b4d4144f79bdd9dd04a287c"},
		{40, "dfa66747de9ae63030ca32611497c827"},
		{64, "51f0bebf7e3b9d92fc49741779363cfe"},
	}

	for _, tc := range cases {
		b, err := k.block(k.KID)
		if err != nil {
			t.Fatal(err)
		}

		if got := hex.EncodeToString(cmac(b, msg[:tc.n])); got != tc.want {
			t.Fatalf("CMAC of %d octets = %s, want %s", tc.n, got, tc.want)
		}
	}
}
