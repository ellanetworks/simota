package ota

import (
	"bytes"
	"errors"
	"fmt"
)

// responseUDH is the user data header of a Response Packet in a single short message: its length, then the RPI as
// IEIa '71' with a null IEDa (TS 31.115 clause 4.4).
var responseUDH = []byte{0x02, 0x71, 0x00}

// Status is the Response Status Code of a Response Packet (TS 102 225 clause 5.2.2, TS 31.115 clause 7).
type Status uint8

const (
	StatusPoROK                     Status = 0x00
	StatusIntegrityFailed           Status = 0x01
	StatusCounterLow                Status = 0x02
	StatusCounterHigh               Status = 0x03
	StatusCounterBlocked            Status = 0x04
	StatusCipheringError            Status = 0x05
	StatusUnidentifiedSecurityError Status = 0x06
	StatusInsufficientMemory        Status = 0x07
	StatusMoreTime                  Status = 0x08
	StatusTARUnknown                Status = 0x09
	StatusInsufficientSecurityLevel Status = 0x0a
	StatusResponseInSubmit          Status = 0x0b
	StatusResponseInUSSD            Status = 0x0c
)

var statusNames = map[Status]string{
	StatusPoROK:                     "por_ok",
	StatusIntegrityFailed:           "rc_cc_ds_failed",
	StatusCounterLow:                "counter_low",
	StatusCounterHigh:               "counter_high",
	StatusCounterBlocked:            "counter_blocked",
	StatusCipheringError:            "ciphering_error",
	StatusUnidentifiedSecurityError: "unidentified_security_error",
	StatusInsufficientMemory:        "insufficient_memory",
	StatusMoreTime:                  "more_time",
	StatusTARUnknown:                "tar_unknown",
	StatusInsufficientSecurityLevel: "insufficient_security_level",
	StatusResponseInSubmit:          "response_in_sms_submit",
	StatusResponseInUSSD:            "response_in_ussd",
}

func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}

	// 'C0' to 'FE' are proprietary; the others are reserved (TS 102 225 table 5).
	if s >= 0xc0 && s <= 0xfe {
		return fmt.Sprintf("proprietary_%02x", byte(s))
	}

	return fmt.Sprintf("reserved_%02x", byte(s))
}

var ErrNotResponse = errors.New("not a response packet")

// Response is a Response Packet.
type Response struct {
	TAR     [3]byte
	Counter uint64
	Status  Status
	// Unsecured is a response without security, which a card that cannot authenticate the Sending Entity may send.
	Unsecured bool
	// Data is the additional response data, such as the response of a remote management script (TS 102 226), without
	// its padding.
	Data []byte
}

// DecodeResponse decodes the Response Packet in the TP-UDH and TP-UD of an SMS-DELIVER-REPORT or SMS-SUBMIT, secured as
// SPI asks (TS 31.115 clause 4.4).
func DecodeResponse(k Keyset, udh, ud []byte) (Response, error) {
	return decodeResponse(k, SPI, udh, ud)
}

// decodeResponse is DecodeResponse of the PoR to a command of another SPI, of which only the PoR's ciphering and
// integrity matter, for the tests' vectors.
func decodeResponse(k Keyset, spi uint16, udh, ud []byte) (Response, error) {
	ciphered, integrity := spi&spiPoRCiphering != 0, spi&spiPoRIntegrity

	// The card cannot tell the ME to set the TP-UDHI of its Response Packet, so a Response Packet may arrive without
	// one, and is then read as if it were set (TS 31.115 clause 4.1).
	if len(udh) == 0 && bytes.HasPrefix(ud, responseUDH) {
		udh, ud = ud[:len(responseUDH)], ud[len(responseUDH):]
	}

	if !bytes.Equal(udh, responseUDH) {
		return Response{}, fmt.Errorf("%w: user data header %x", ErrNotResponse, udh)
	}

	// RPL 2, RHL 1, TAR 3.
	if len(ud) < 6 {
		return Response{}, fmt.Errorf("response: %d octets is too short", len(ud))
	}

	rpl := int(ud[0])<<8 | int(ud[1])
	if rpl != len(ud)-2 {
		return Response{}, fmt.Errorf("response: RPL %d does not match %d octets", rpl, len(ud)-2)
	}

	rhl := int(ud[2])
	header := ud[:6]
	body := ud[6:]

	// A card that cannot authenticate the Sending Entity may answer with an error status, no security and a zero
	// counter (TS 102 225 clause 4.1).
	if rhl == 10 && len(body) == counterLength+2 && Status(body[counterLength+1]) != StatusPoROK &&
		bytes.Equal(body[:counterLength], make([]byte, counterLength)) && (ciphered || integrity != 0) {
		r := Response{Status: Status(body[counterLength+1]), Unsecured: true}
		copy(r.TAR[:], header[3:6])

		return r, nil
	}

	ccLen := 0
	if integrity == spiPoRCC {
		ccLen = k.ccLen()
	} else if integrity != 0 {
		return Response{}, errors.New("response: only a cryptographic checksum or no integrity is supported")
	}

	// RHL counts TAR to the end of the RC/CC/DS: TAR 3, CNTR 5, PCNTR 1, status 1.
	if rhl != 10+ccLen {
		return Response{}, fmt.Errorf("response: RHL %d, want %d", rhl, 10+ccLen)
	}

	if ciphered {
		var err error

		body, err = k.decrypt(body)
		if err != nil {
			return Response{}, fmt.Errorf("response: %w", err)
		}
	}

	if len(body) < counterLength+2+ccLen {
		return Response{}, fmt.Errorf("response: %d octets after the TAR is too short", len(body))
	}

	r := Response{Status: Status(body[counterLength+1])}
	copy(r.TAR[:], header[3:6])

	for _, b := range body[:counterLength] {
		r.Counter = r.Counter<<8 | uint64(b)
	}

	padding := int(body[counterLength])
	cc := body[counterLength+2 : counterLength+2+ccLen]
	data := body[counterLength+2+ccLen:]

	if ccLen > 0 {
		// The UDH, RPL and RHL are part of the checksum in SMS (TS 31.115 clause 4.4).
		if err := k.verify(concat(responseUDH, header, body[:counterLength+2], data), cc); err != nil {
			return Response{}, fmt.Errorf("response: %w", err)
		}
	}

	if padding > len(data) {
		return Response{}, fmt.Errorf("response: %d padding octets in %d octets of data", padding, len(data))
	}

	r.Data = data[:len(data)-padding]

	return r, nil
}
