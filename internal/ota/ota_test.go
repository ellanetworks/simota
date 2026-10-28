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

// secured is SPI without the counter, that of the vectors of the SJA5 and of pySim: a ciphered command with a CC that
// asks for a ciphered PoR with a CC. signed asks for a PoR with a CC, not ciphered, and plain for an unsecured PoR.
const (
	secured uint16 = 0x0619
	signed  uint16 = 0x0609
	plain   uint16 = 0x0601
)

// TestSPI checks that SPI is valid and what simota needs (TS 102 225 clause 5.1.1): a ciphered command with a
// cryptographic checksum and a counter that must be higher, which AES requires (clause 5.1.2); a PoR always, of the
// command's integrity, which a ciphered PoR needs ciphered with a cryptographic checksum, in the SMS-DELIVER-REPORT.
func TestSPI(t *testing.T) {
	first, second := byte(SPI>>8), byte(SPI&0xff)

	for _, c := range []struct {
		name string
		got  byte
		want byte
	}{
		{"integrity: cryptographic checksum", first & 0x03, 0x02},
		{"ciphering", first & 0x04, 0x04},
		{"counter higher than the card's", first >> 3 & 0x03, 0x02},
		{"first octet reserved bits", first >> 5, 0},
		{"PoR always", second & 0x03, 0x01},
		{"PoR integrity of the command's", second >> 2 & 0x03, first & 0x03},
		{"PoR ciphered", second & 0x10, 0x10},
		{"PoR in the SMS-DELIVER-REPORT", second & 0x20, 0},
		{"second octet reserved bits", second >> 6, 0},
	} {
		if c.got != c.want {
			t.Errorf("SPI %04x: %s: %#x, want %#x", SPI, c.name, c.got, c.want)
		}
	}
}

// encodeResponse codes r as the card would answer a command of spi: the TP-UDH and TP-UD of its SMS-DELIVER-REPORT
// (TS 31.115 clause 4.4).
func encodeResponse(t *testing.T, k Keyset, spi uint16, r Response) (udh, ud []byte) {
	t.Helper()

	ccLen := 0
	if spi&spiPoRIntegrity == spiPoRCC {
		ccLen = k.ccLen()
	}

	data := r.Data
	padding := 0

	if spi&spiPoRCiphering != 0 {
		if rem := (counterLength + 2 + ccLen + len(data)) % k.blockSize(); rem != 0 {
			padding = k.blockSize() - rem
			data = append(append([]byte{}, data...), make([]byte, padding)...)
		}
	}

	counter := make([]byte, counterLength+2)
	for i := range counterLength {
		counter[i] = byte(r.Counter >> (8 * (counterLength - 1 - i)))
	}

	counter[counterLength], counter[counterLength+1] = byte(padding), byte(r.Status)

	rpl := 1 + 3 + len(counter) + ccLen + len(data)
	header := []byte{byte(rpl >> 8), byte(rpl), byte(10 + ccLen), r.TAR[0], r.TAR[1], r.TAR[2]}

	var (
		cc  []byte
		err error
	)

	if ccLen > 0 {
		if cc, err = k.checksum(concat(responseUDH, header, counter, data)); err != nil {
			t.Fatal(err)
		}
	}

	body := concat(counter, cc, data)

	if spi&spiPoRCiphering != 0 {
		if body, err = k.encrypt(body); err != nil {
			t.Fatal(err)
		}
	}

	return append([]byte{}, responseUDH...), concat(header, body)
}

var simRFM = [3]byte{0xb0, 0x00, 0x10}

func TestEncodeCommand(t *testing.T) {
	cases := []struct {
		name    string
		spi     uint16
		command Command
		want    string
	}{
		{
			name:    "select DF.GSM, get response",
			spi:     secured,
			command: Command{Keyset: sja5(t), TAR: simRFM, Data: mustHex(t, "a0a40000027f20a0c0000016")},
			want:    "00281506191515b00010cf71695f686b9516cfee5d0194c605ac9e8baa3b77f0144614068eb3ab557f93",
		},
		{
			name:    "read EF.OPLMNwAcT",
			spi:     secured,
			command: Command{Keyset: sja5(t), TAR: simRFM, Data: mustHex(t, "a0a40000027f20a0a40000026f61a0b000000f")},
			want:    "00301506191515b000109a46af5dbcd005a46055f55440c44512bd230ba12854b987522f687b50bc495d8638822e5edaef0e",
		},
		{
			name:    "counter",
			spi:     SPI,
			command: Command{Keyset: sja5(t), TAR: simRFM, Counter: 100, Data: mustHex(t, "a0a40000027f20a0c0000016")},
			want:    "00281516191515b000108fb60fad7125e0ee4811b11427719bef3eb8b1ef6ecb1b4f326272f85256243e",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			udh, ud, err := tc.command.encode(tc.spi)
			if err != nil {
				t.Fatalf("encode: %v", err)
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
// forbids with AES and SPI always has.
func TestEncodeCommandAES(t *testing.T) {
	c := Command{Keyset: pysimAES(t), TAR: [3]byte{0xb0, 0x00, 0x11}, Data: mustHex(t, "00a40004023f00")}

	_, ud, err := c.encode(secured)
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

	for name, c := range map[string]Command{
		"key version 0":     {Keyset: Keyset{Algorithm: TripleDES, KIc: sja5(t).KIc, KID: sja5(t).KID}},
		"short KID":         {Keyset: short},
		"unknown algorithm": {Keyset: Keyset{Version: 1, Algorithm: 0x9, KIc: sja5(t).KIc, KID: sja5(t).KID}},
		"counter overflow":  {Keyset: sja5(t), Counter: 1 << 40},
	} {
		if _, _, err := c.Encode(); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

// TestEncodeCommandCipheredWithoutCC checks pySim's ciphered command without CC, of SPI '04 19'. pySim sets the unused
// KID to the key set; it is zero here (TS 102 225 clause 5.1).
func TestEncodeCommandCipheredWithoutCC(t *testing.T) {
	c := Command{Keyset: pysim3DES(t), TAR: [3]byte{0xb0, 0x00, 0x00}, Data: mustHex(t, "00a40000023f00")}

	_, ud, err := c.encode(0x0419)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if got, want := hex.EncodeToString(ud), "00180d04193500b00000e3ec80a849b554421276af3883927c20"; got != want {
		t.Fatalf("ud = %s, want %s", got, want)
	}
}

// TestCCLength checks an AES key set with 4-octet checksums: CHL and RHL shrink, and the checksum is the first four
// octets of the CMAC (TS 102 225 clause 5.1.3.1).
func TestCCLength(t *testing.T) {
	k := pysimAES(t)
	k.CCLength = 4

	c := Command{Keyset: k, TAR: [3]byte{0xb0, 0x00, 0x01}, Counter: 7, Data: mustHex(t, "00a4000c026f61")}

	_, ud, err := c.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if ud[2] != 13+4 {
		t.Fatalf("CHL = %d, want 17", ud[2])
	}

	udh, rud := encodeResponse(t, k, SPI, Response{TAR: c.TAR, Counter: 7, Data: mustHex(t, "019000")})
	if rud[2] != 10+4 {
		t.Fatalf("RHL = %d, want 14", rud[2])
	}

	r, err := DecodeResponse(k, udh, rud)
	if err != nil || r.Status != StatusPoROK || r.Counter != 7 || hex.EncodeToString(r.Data) != "019000" {
		t.Fatalf("response = %+v, %v", r, err)
	}

	for _, bad := range []int{2, 16} {
		k.CCLength = bad
		if _, _, err := (Command{Keyset: k, Counter: 1}).Encode(); err == nil {
			t.Fatalf("CC length %d: expected an error", bad)
		}
	}
}

func TestStatusString(t *testing.T) {
	for s, want := range map[Status]string{0x00: "por_ok", 0x0b: "response_in_sms_submit", 0x0d: "reserved_0d", 0xc1: "proprietary_c1", 0xff: "reserved_ff"} {
		if got := s.String(); got != want {
			t.Fatalf("%#x = %s, want %s", byte(s), got, want)
		}
	}
}

func TestEncodeCommandTooLong(t *testing.T) {
	c := Command{Keyset: sja5(t), TAR: simRFM, Data: make([]byte, 120)}
	if _, _, err := c.Encode(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDecodeResponse(t *testing.T) {
	cases := []struct {
		name     string
		keyset   Keyset
		spi      uint16
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

			r, err := decodeResponse(tc.keyset, tc.spi, p[:3], p[3:])
			if err != nil {
				t.Fatalf("decodeResponse: %v", err)
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

	r, err := decodeResponse(sja5(t), secured, nil, p)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}

	if r.Status != StatusPoROK || len(r.Data) < 3 {
		t.Fatalf("response = %+v", r)
	}
}

// TestEncodeResponse reproduces the PoRs of the SJA5 and of pySim's tests from their decoded content, which checks
// the tests' encodeResponse.
func TestEncodeResponse(t *testing.T) {
	for _, tc := range []struct {
		keyset Keyset
		spi    uint16
		packet string
	}{
		{sja5(t), secured, "027100002c12b0001023dd41b55fb14f5e7726736aeb0bd69e4743f58b520e4f3bc42ca3f66b789b404fb60c953eff1625"},
		{pysim3DES(t), secured, "027100001c12b000119660ebdb81be189b5e4389e9e7ab2bc0954f963ad869ed7c"},
		{pysim3DES(t), signed, "027100001612b000110000000000000055f47118381175fb01612f"},
		{pysimAES(t), secured, "027100002412b00011ebc6b497e2cad7aedf36ace0e3a29b38853f0fe9ccde81913be5702b73abce1f"},
	} {
		p := mustHex(t, tc.packet)

		r, err := decodeResponse(tc.keyset, tc.spi, p[:3], p[3:])
		if err != nil {
			t.Fatalf("decodeResponse %s: %v", tc.packet, err)
		}

		udh, ud := encodeResponse(t, tc.keyset, tc.spi, r)

		if got := hex.EncodeToString(append(udh, ud...)); got != tc.packet {
			t.Fatalf("encodeResponse = %s, want %s", got, tc.packet)
		}
	}
}

func TestDecodeResponseRejectsBadChecksum(t *testing.T) {
	p := mustHex(t, "027100001612b000110000000000000055f47118381175fb02612f")
	if _, err := decodeResponse(pysim3DES(t), signed, p[:3], p[3:]); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("err = %v, want ErrIntegrity", err)
	}
}

func TestDecodeResponseRejectsCommand(t *testing.T) {
	p := mustHex(t, "02700000281506191515b00010cf71695f686b9516cfee5d0194c605ac9e8baa3b77f0144614068eb3ab557f93")
	if _, err := DecodeResponse(sja5(t), p[:3], p[3:]); !errors.Is(err, ErrNotResponse) {
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
