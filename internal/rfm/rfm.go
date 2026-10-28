// Package rfm builds the command scripts of Remote File Management in the compact remote application data format, and
// decodes their responses (ETSI TS 102 226 clause 5.1).
package rfm

import (
	"errors"
	"fmt"
)

// Class is the class byte of a script's commands: GSM for the SIM RFM application, such as TAR 'B00010', and UICC for
// a UICC or USIM RFM application.
type Class byte

const (
	ClassGSM  Class = 0xa0
	ClassUICC Class = 0x00
)

const (
	insSelect       = 0xa4
	insReadBinary   = 0xb0
	insUpdateBinary = 0xd6
	insGetResponse  = 0xc0
)

// Script is a command string: commands coded as T = 0 TPDU commands, one after the other (TS 102 226 clause 5.1.1).
type Script struct {
	class    Class
	commands [][]byte
	// final is whether the script ends with its one case 2 command or GET RESPONSE, after which no command may follow.
	final bool
}

func NewScript(class Class) *Script {
	return &Script{class: class}
}

// Select selects a file by its identifier.
func (s *Script) Select(fid uint16) *Script {
	if s.class == ClassUICC {
		// Selection by file identifier, no data returned (TS 102 221 clause 11.1.1).
		return s.add(insSelect, 0x00, 0x0c, []byte{byte(fid >> 8), byte(fid)})
	}

	return s.add(insSelect, 0x00, 0x00, []byte{byte(fid >> 8), byte(fid)})
}

// UpdateBinary writes data at offset in the selected transparent file.
func (s *Script) UpdateBinary(offset uint16, data []byte) *Script {
	return s.add(insUpdateBinary, byte(offset>>8), byte(offset), data)
}

// ReadBinary reads n octets at offset of the selected transparent file, or all available ones for n = 0. It must be
// the last command of the script.
func (s *Script) ReadBinary(offset uint16, n uint8) *Script {
	return s.addFinal(insReadBinary, byte(offset>>8), byte(offset), n)
}

// GetResponse retrieves n octets of the response of the previous command, or all for n = 0. It must be the last
// command of the script.
func (s *Script) GetResponse(n uint8) *Script {
	return s.addFinal(insGetResponse, 0, 0, n)
}

func (s *Script) add(ins, p1, p2 byte, data []byte) *Script {
	cmd := []byte{byte(s.class), ins, p1, p2, byte(len(data))}
	s.commands = append(s.commands, append(cmd, data...))

	return s
}

func (s *Script) addFinal(ins, p1, p2, le byte) *Script {
	s.commands = append(s.commands, []byte{byte(s.class), ins, p1, p2, le})
	s.final = true

	return s
}

var ErrScript = errors.New("invalid script")

// Bytes is the command string.
func (s *Script) Bytes() ([]byte, error) {
	if len(s.commands) == 0 {
		return nil, fmt.Errorf("%w: no command", ErrScript)
	}

	for i, c := range s.commands {
		if len(c) > 5+255 {
			return nil, fmt.Errorf("%w: command %d has more than 255 octets of data", ErrScript, i+1)
		}

		ins := c[1]
		if (ins == insReadBinary || ins == insGetResponse) && i != len(s.commands)-1 {
			// TS 102 226 clause 5.1.1.
			return nil, fmt.Errorf("%w: command %d must be the last", ErrScript, i+1)
		}
	}

	var out []byte
	for _, c := range s.commands {
		out = append(out, c...)
	}

	return out, nil
}

// Response is the additional response data of a script in the compact format (TS 102 226 clause 5.1.2).
type Response struct {
	// Executed is the number of commands executed.
	Executed int
	// StatusWord is the status bytes, or '61 xx' procedure bytes, of the last executed command.
	StatusWord uint16
	// Data is the response data of the last command, when it is a case 2 command or GET RESPONSE.
	Data []byte
}

// OK reports whether the last executed command completed normally: '90 00', '91 xx' or the '61 xx' of response data
// to retrieve (TS 102 221 clause 10.2.1).
func (r Response) OK() bool {
	return r.StatusWord == 0x9000 || r.StatusWord>>8 == 0x91 || r.StatusWord>>8 == 0x61 || r.StatusWord>>8 == 0x9f
}

func DecodeResponse(b []byte) (Response, error) {
	if len(b) < 3 {
		return Response{}, fmt.Errorf("compact response of %d octets is too short", len(b))
	}

	return Response{Executed: int(b[0]), StatusWord: uint16(b[1])<<8 | uint16(b[2]), Data: b[3:]}, nil
}
