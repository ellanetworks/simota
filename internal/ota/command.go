package ota

import (
	"errors"
	"fmt"
)

// TS 23.040 clause 9.2.3.24
const maxUserData = 140

// TS 31.115 clause 4.2
var commandUDH = []byte{0x02, 0x70, 0x00}

// ETSI TS 102 225 clause 5.1
const counterLength = 5

type Command struct {
	Keyset  Keyset
	TAR     [3]byte
	Counter uint64
	Data    []byte
}

// TS 31.115 clause 4.2
func (c Command) Encode() (udh, ud []byte, err error) {
	if err := c.Keyset.validate(); err != nil {
		return nil, nil, fmt.Errorf("command: %w", err)
	}

	return c.encode(SPI)
}

func (c Command) encode(spi uint16) (udh, ud []byte, err error) {
	if c.Counter >= 1<<(8*counterLength) {
		return nil, nil, errors.New("command: counter exceeds 5 octets")
	}

	ciphered, checksummed := spi&spiCiphering != 0, spi&spiIntegrity == spiCC

	ccLen := 0
	if checksummed {
		ccLen = c.Keyset.ccLen()
	}

	data := c.Data
	padding := 0

	if ciphered {
		if r := (counterLength + 1 + ccLen + len(data)) % c.Keyset.blockSize(); r != 0 {
			padding = c.Keyset.blockSize() - r
			data = append(append([]byte{}, data...), make([]byte, padding)...)
		}
	}

	chl := 13 + ccLen
	cpl := 1 + chl + len(data)

	header := []byte{
		byte(cpl >> 8), byte(cpl), byte(chl), byte(spi >> 8), byte(spi), c.Keyset.keyID(ciphered),
		c.Keyset.keyID(checksummed), c.TAR[0], c.TAR[1], c.TAR[2],
	}

	counter := make([]byte, counterLength+1)
	for i := range counterLength {
		counter[i] = byte(c.Counter >> (8 * (counterLength - 1 - i)))
	}

	counter[counterLength] = byte(padding)

	var cc []byte

	if ccLen > 0 {
		cc, err = c.Keyset.checksum(concat(header, counter, data))
		if err != nil {
			return nil, nil, fmt.Errorf("command: %w", err)
		}
	}

	secured := concat(counter, cc, data)

	if ciphered {
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

// ETSI TS 102 225 clause 5.1
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
