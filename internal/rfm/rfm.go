// Package rfm builds the command scripts of Remote File Management in the compact remote application data format,
// and checks their responses (ETSI TS 102 226 clause 5.1, 3GPP TS 31.116 clause 5).
package rfm

import (
	"errors"
	"fmt"
)

// classUICC is the class byte of a script's commands, for an ADF or UICC Shared File System RFM application, such as
// one linked to the USIM (TS 102 226 clause 5.1.1, TS 102 221 clause 10.1.1).
const classUICC = 0x00

const (
	insSelect       = 0xa4
	insReadBinary   = 0xb0
	insUpdateBinary = 0xd6
	insGetResponse  = 0xc0
)

// maxOffset is the largest offset of READ BINARY and UPDATE BINARY with P1 b8 = 0: P1 b8 = 1 selects an EF by its SFI
// (TS 102 221 table 11.10).
const maxOffset = 0x7fff

// kind is the ISO/IEC 7816-3 case of a command, which tells its expected answer.
type kind int

const (
	// caseNoData is a command without response data: case 1 or 3.
	caseNoData kind = iota
	// caseData is a command with response data: case 2.
	caseData
	// caseDataPending is a case 4 command, whose response data a GET RESPONSE retrieves.
	caseDataPending
)

type command struct {
	apdu []byte
	kind kind
}

// Script is a command string: commands coded as T = 0 TPDU commands, one after the other (TS 102 226 clause 5.1.1).
type Script struct {
	commands []command
	err      error
}

func NewScript() *Script {
	return &Script{}
}

// Select selects a file by its identifier, without response data (TS 102 221 clause 11.1.1, tables 11.1 and 11.2).
func (s *Script) Select(fid uint16) *Script {
	return s.add(caseNoData, insSelect, 0x00, 0x0c, []byte{byte(fid >> 8), byte(fid)})
}

// SelectFCP selects a file by its identifier and asks for its FCP template, which the GET RESPONSE that must follow
// returns (TS 102 221 clause 11.1.1, table 11.2; TS 102 226 clause 5.1.1).
func (s *Script) SelectFCP(fid uint16) *Script {
	return s.add(caseDataPending, insSelect, 0x00, 0x04, []byte{byte(fid >> 8), byte(fid)})
}

// UpdateBinary writes data at offset in the selected transparent file.
func (s *Script) UpdateBinary(offset uint16, data []byte) *Script {
	if offset > maxOffset {
		s.fail(fmt.Errorf("offset %#x exceeds %#x", offset, maxOffset))
		return s
	}

	if len(data) == 0 || len(data) > 255 {
		s.fail(fmt.Errorf("an update writes 1 to 255 octets, not %d", len(data)))
		return s
	}

	return s.add(caseNoData, insUpdateBinary, byte(offset>>8), byte(offset), data)
}

// ReadBinary reads n octets at offset of the selected transparent file. It must be the last command of the script.
func (s *Script) ReadBinary(offset uint16, n uint8) *Script {
	if offset > maxOffset {
		s.fail(fmt.Errorf("offset %#x exceeds %#x", offset, maxOffset))
		return s
	}

	if n == 0 {
		// P3 '00' is "all data" (TS 102 226 clause 5.1.1): a length is always given.
		s.fail(errors.New("a read needs a length"))
		return s
	}

	return s.addLe(insReadBinary, byte(offset>>8), byte(offset), n)
}

// GetResponse retrieves the response data of the previous case 4 command. It must be the last command of the script.
func (s *Script) GetResponse() *Script {
	// P3 '00' returns all the available data for an ADF or UICC RFM application (TS 102 226 clause 5.1.1).
	return s.addLe(insGetResponse, 0, 0, 0)
}

func (s *Script) add(k kind, ins, p1, p2 byte, data []byte) *Script {
	apdu := append([]byte{classUICC, ins, p1, p2, byte(len(data))}, data...)
	s.commands = append(s.commands, command{apdu: apdu, kind: k})

	return s
}

func (s *Script) addLe(ins, p1, p2, le byte) *Script {
	s.commands = append(s.commands, command{apdu: []byte{classUICC, ins, p1, p2, le}, kind: caseData})

	return s
}

func (s *Script) fail(err error) {
	if s.err == nil {
		s.err = err
	}
}

var ErrScript = errors.New("invalid script")

// Bytes is the command string.
func (s *Script) Bytes() ([]byte, error) {
	if s.err != nil {
		return nil, fmt.Errorf("%w: %w", ErrScript, s.err)
	}

	if len(s.commands) == 0 {
		return nil, fmt.Errorf("%w: no command", ErrScript)
	}

	for i, c := range s.commands {
		// A case 2 command, GET RESPONSE included, occurs once and last (TS 102 226 clause 5.1.1, TS 31.116 clause 5.1).
		if c.kind == caseData && i != len(s.commands)-1 {
			return nil, fmt.Errorf("%w: command %d must be the last", ErrScript, i+1)
		}

		if c.apdu[1] == insGetResponse && (i == 0 || s.commands[i-1].kind != caseDataPending) {
			return nil, fmt.Errorf("%w: GET RESPONSE must follow a command with response data pending", ErrScript)
		}
	}

	var out []byte
	for _, c := range s.commands {
		out = append(out, c.apdu...)
	}

	return out, nil
}

// Len is the number of commands.
func (s *Script) Len() int {
	return len(s.commands)
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

func DecodeResponse(b []byte) (Response, error) {
	if len(b) < 3 {
		return Response{}, fmt.Errorf("compact response of %d octets is too short", len(b))
	}

	return Response{Executed: int(b[0]), StatusWord: uint16(b[1])<<8 | uint16(b[2]), Data: b[3:]}, nil
}

// Result is a script's outcome.
type Result struct {
	// Warning is a status word that completed the last command with a warning, such as '62 82' or '63 CX' (TS 102 221
	// table 10.9); the command took effect.
	Warning bool
}

// ScriptError is a script that stopped before its end, or whose last command failed.
type ScriptError struct {
	Executed, Commands int
	StatusWord         uint16
}

func (e *ScriptError) Error() string {
	if e.Executed < e.Commands {
		return fmt.Sprintf("command %d of %d failed with status word %04x", e.Executed, e.Commands, e.StatusWord)
	}

	return fmt.Sprintf("command %d failed with status word %04x", e.Executed, e.StatusWord)
}

// Check tells whether the card ran every command of s and the last one completed, by its kind and status word.
func (s *Script) Check(r Response) (Result, error) {
	fail := &ScriptError{Executed: r.Executed, Commands: len(s.commands), StatusWord: r.StatusWord}
	if r.Executed != len(s.commands) || len(s.commands) == 0 {
		return Result{}, fail
	}

	sw1 := byte(r.StatusWord >> 8)
	last := s.commands[len(s.commands)-1].kind

	switch {
	case r.StatusWord == 0x9000:
	case sw1 == 0x91:
		// Normal ending, with a proactive command pending (TS 102 221 clause 10.2.1.1).
	case last == caseDataPending && sw1 == 0x61:
		// Response data available (TS 102 221 clause 10.2.1.1).
	case sw1 == 0x62 || sw1 == 0x63:
		// Warnings: processing completed (TS 102 221 clause 10.2.1.2, TS 102 226 clause 4).
		return Result{Warning: true}, nil
	default:
		return Result{}, fail
	}

	if last == caseData && len(r.Data) == 0 {
		return Result{}, fmt.Errorf("command %d returned no data", r.Executed)
	}

	return Result{}, nil
}

// FileSize is the size of a transparent EF in its FCP template: data object '80' in template '62' (TS 102 221 clause
// 11.1.1.3).
func FileSize(fcp []byte) (int, error) {
	if len(fcp) < 2 || fcp[0] != 0x62 || int(fcp[1]) != len(fcp)-2 || fcp[1] >= 0x80 {
		return 0, fmt.Errorf("not an FCP template: %x", fcp)
	}

	for b := fcp[2:]; len(b) >= 2; {
		tag, n := b[0], int(b[1])
		if 2+n > len(b) {
			return 0, fmt.Errorf("FCP data object %#02x overruns the template", tag)
		}

		if tag == 0x80 {
			size := 0
			for _, o := range b[2 : 2+n] {
				size = size<<8 | int(o)
			}

			return size, nil
		}

		b = b[2+n:]
	}

	return 0, errors.New("FCP template without file size")
}
