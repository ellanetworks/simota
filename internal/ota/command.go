package ota

import (
	"errors"
	"fmt"
)

// maxUserData is the user data of a single SMS of 8-bit data (TS 23.040 clause 9.2.3.24).
const maxUserData = 140

// commandUDH is the user data header of a Command Packet in a single short message: its length, then the CPI as
// IEIa '70' with a null IEDa (TS 31.115 clause 4.2).
var commandUDH = []byte{0x02, 0x70, 0x00}

// counterLength is the length of CNTR (TS 102 225 clause 5.1).
const counterLength = 5

// Command is an application message for the card application at TAR, secured with a key set.
type Command struct {
	Keyset Keyset
	SPI    SPI
	TAR    [3]byte
	// Counter is the CNTR. It must be zero when the SPI asks for no counter.
	Counter uint64
	// Data is the application message, such as a remote management script (TS 102 226).
	Data []byte
}

// Encode returns the TP-UDH and the TP-UD of the SMS-DELIVER that carries c in a single short message (TS 31.115
// clause 4.2).
func (c Command) Encode() (udh, ud []byte, err error) {
	if err := c.Keyset.validate(); err != nil {
		return nil, nil, fmt.Errorf("command: %w", err)
	}

	spi, err := c.SPI.encode()
	if err != nil {
		return nil, nil, fmt.Errorf("command: %w", err)
	}

	if c.SPI.Integrity != IntegrityNone && c.SPI.Integrity != IntegrityCC {
		return nil, nil, errors.New("command: only a cryptographic checksum or no integrity is supported")
	}

	if c.Keyset.Algorithm == AES && c.SPI.Counter != CounterHigher && c.SPI.Counter != CounterOneHigher {
		// TS 102 225 clauses 5.1.2, 5.1.3.1.
		return nil, nil, errors.New("command: AES needs a counter that must be higher or one higher")
	}

	return c.encode(spi)
}

// encode is Encode without the checks of the SPI against the key set.
func (c Command) encode(spi [2]byte) (udh, ud []byte, err error) {
	if c.SPI.Counter == CounterNone && c.Counter != 0 {
		return nil, nil, errors.New("command: counter set while the SPI asks for none")
	}

	if c.Counter >= 1<<(8*counterLength) {
		return nil, nil, errors.New("command: counter exceeds 5 octets")
	}

	ccLen := 0
	if c.SPI.Integrity == IntegrityCC {
		ccLen = ccLength
	}

	data := c.Data
	padding := 0

	if c.SPI.Ciphering {
		// Ciphering covers CNTR, PCNTR, the CC and the secured data.
		if r := (counterLength + 1 + ccLen + len(data)) % c.Keyset.blockSize(); r != 0 {
			padding = c.Keyset.blockSize() - r
			data = append(append([]byte{}, data...), make([]byte, padding)...)
		}
	}

	// CHL counts SPI to the end of the RC/CC/DS: SPI 2, KIc 1, KID 1, TAR 3, CNTR 5, PCNTR 1.
	chl := 13 + ccLen
	cpl := 1 + chl + len(data)

	header := []byte{
		byte(cpl >> 8), byte(cpl), byte(chl), spi[0], spi[1], c.Keyset.keyID(c.SPI.Ciphering),
		c.Keyset.keyID(c.SPI.Integrity == IntegrityCC), c.TAR[0], c.TAR[1], c.TAR[2],
	}

	counter := make([]byte, counterLength+1)
	for i := range counterLength {
		counter[i] = byte(c.Counter >> (8 * (counterLength - 1 - i)))
	}

	counter[counterLength] = byte(padding)

	var cc []byte

	if ccLen > 0 {
		// CPL and CHL are part of the checksum in SMS (TS 31.115 clause 4.2).
		cc, err = c.Keyset.checksum(concat(header, counter, data))
		if err != nil {
			return nil, nil, fmt.Errorf("command: %w", err)
		}
	}

	secured := concat(counter, cc, data)

	if c.SPI.Ciphering {
		secured, err = c.Keyset.encrypt(secured)
		if err != nil {
			return nil, nil, fmt.Errorf("command: %w", err)
		}
	}

	ud = concat(header, secured)
	if len(commandUDH)+len(ud) > maxUserData {
		return nil, nil, fmt.Errorf("command: packet of %d octets does not fit a single short message", len(commandUDH)+len(ud))
	}

	return append([]byte{}, commandUDH...), ud, nil
}

// keyID is KIc or KID: the key version and the algorithm, or zero when unused (TS 102 225 clause 5.1).
func (k Keyset) keyID(used bool) byte {
	if !used {
		return 0
	}

	return k.Version<<4 | byte(k.Algorithm)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}

	return out
}
