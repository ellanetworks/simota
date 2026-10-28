// Package ota codes the secured packets of SIM OTA over SMS point to point: the Command Packet that the Sending Entity
// sends in an SMS-DELIVER, and the Response Packet (PoR) that the card answers with (ETSI TS 102 225, 3GPP TS 31.115).
package ota

import (
	"errors"
	"fmt"
)

// Integrity is the redundancy check, cryptographic checksum or digital signature of a packet (TS 102 225 clause
// 5.1.1).
type Integrity uint8

const (
	IntegrityNone Integrity = 0
	IntegrityRC   Integrity = 1
	IntegrityCC   Integrity = 2
	IntegrityDS   Integrity = 3
)

// CounterMode is how the card checks the counter of a Command Packet (TS 102 225 clause 5.1.1).
type CounterMode uint8

const (
	CounterNone CounterMode = 0
	// CounterAvailable makes the counter available for information, without replay checking.
	CounterAvailable CounterMode = 1
	// CounterHigher requires the counter to be higher than the card's.
	CounterHigher CounterMode = 2
	// CounterOneHigher requires the counter to be one higher than the card's.
	CounterOneHigher CounterMode = 3
)

// PoRMode is whether the card answers with a Proof of Receipt (TS 102 225 clause 5.1.1).
type PoRMode uint8

const (
	PoRNone    PoRMode = 0
	PoRAlways  PoRMode = 1
	PoROnError PoRMode = 2
)

// SPI is the Security Parameter Indicator of a Command Packet (TS 102 225 clause 5.1.1, TS 31.115 clause 4.2).
type SPI struct {
	Integrity Integrity
	Ciphering bool
	Counter   CounterMode
	PoR       PoRMode
	// PoRIntegrity, PoRCiphering are the security of the Response Packet.
	PoRIntegrity Integrity
	PoRCiphering bool
	// PoRInSubmit is the PoR sent in an SMS-SUBMIT instead of the SMS-DELIVER-REPORT (TS 31.115 clause 4.2).
	PoRInSubmit bool
}

func (s SPI) encode() ([2]byte, error) {
	if s.Integrity > IntegrityDS || s.PoRIntegrity > IntegrityDS || s.Counter > CounterOneHigher || s.PoR > PoROnError {
		return [2]byte{}, fmt.Errorf("spi: value out of range: %+v", s)
	}

	first := byte(s.Integrity) | byte(s.Counter)<<3
	if s.Ciphering {
		first |= 0x04
	}

	second := byte(s.PoR) | byte(s.PoRIntegrity)<<2
	if s.PoRCiphering {
		second |= 0x10
	}

	if s.PoRInSubmit {
		second |= 0x20
	}

	return [2]byte{first, second}, nil
}

// validatePoR checks the security of the PoR against that of the command: when the PoR has a redundancy check,
// cryptographic checksum or digital signature, it is the command's kind; and a ciphered PoR needs a command with a
// cryptographic checksum or digital signature, and ciphered (TS 102 225 clause 5.1.1).
func (s SPI) validatePoR() error {
	if s.PoRIntegrity != IntegrityNone && s.PoRIntegrity != s.Integrity {
		return errors.New("spi: the PoR's integrity must be that of the command")
	}

	if s.PoRCiphering && (!s.Ciphering || (s.Integrity != IntegrityCC && s.Integrity != IntegrityDS)) {
		return errors.New("spi: a ciphered PoR needs a ciphered command with a cryptographic checksum or signature")
	}

	return nil
}
