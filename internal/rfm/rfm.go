package rfm

import (
	"errors"
	"fmt"
)

// ETSI TS 102 226 clause 5.1.1
// ETSI TS 102 221 clause 10.1.1
const classUICC = 0x00

const (
	insSelect       = 0xa4
	insReadBinary   = 0xb0
	insUpdateBinary = 0xd6
	insGetResponse  = 0xc0
)

// ETSI TS 102 221 clause 11.1.3.2 table 11.10
const maxOffset = 0x7fff

// ISO/IEC 7816-3
type kind int

const (
	caseNoData kind = iota
	caseData
	caseDataPending
)

type command struct {
	apdu []byte
	kind kind
}

// ETSI TS 102 226 clause 5.1.1
type Script struct {
	commands []command
	err      error
}

func NewScript() *Script {
	return &Script{}
}

// ETSI TS 102 221 clause 11.1.1 table 11.1
// ETSI TS 102 221 clause 11.1.1 table 11.2
func (s *Script) Select(fid uint16) *Script {
	return s.add(caseNoData, insSelect, 0x00, 0x0c, []byte{byte(fid >> 8), byte(fid)})
}

// ETSI TS 102 221 clause 11.1.1 table 11.2
// ETSI TS 102 226 clause 5.1.1
func (s *Script) SelectFCP(fid uint16) *Script {
	return s.add(caseDataPending, insSelect, 0x00, 0x04, []byte{byte(fid >> 8), byte(fid)})
}

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

func (s *Script) ReadBinary(offset uint16, n uint8) *Script {
	if offset > maxOffset {
		s.fail(fmt.Errorf("offset %#x exceeds %#x", offset, maxOffset))
		return s
	}

	if n == 0 {
		s.fail(errors.New("a read needs a length"))
		return s
	}

	return s.addLe(insReadBinary, byte(offset>>8), byte(offset), n)
}

// ETSI TS 102 226 clause 5.1.1
func (s *Script) GetResponse() *Script {
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

func (s *Script) Bytes() ([]byte, error) {
	if s.err != nil {
		return nil, fmt.Errorf("%w: %w", ErrScript, s.err)
	}

	if len(s.commands) == 0 {
		return nil, fmt.Errorf("%w: no command", ErrScript)
	}

	for i, c := range s.commands {
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

func (s *Script) Len() int {
	return len(s.commands)
}

// ETSI TS 102 226 clause 5.1.2
type Response struct {
	Executed   int
	StatusWord uint16
	Data       []byte
}

func DecodeResponse(b []byte) (Response, error) {
	if len(b) < 3 {
		return Response{}, fmt.Errorf("compact response of %d octets is too short", len(b))
	}

	return Response{Executed: int(b[0]), StatusWord: uint16(b[1])<<8 | uint16(b[2]), Data: b[3:]}, nil
}

type Result struct {
	Warning bool
}

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
	case last == caseDataPending && sw1 == 0x61:
	case sw1 == 0x62 || sw1 == 0x63:
		return Result{Warning: true}, nil
	default:
		return Result{}, fail
	}

	if last == caseData && len(r.Data) == 0 {
		return Result{}, fmt.Errorf("command %d returned no data", r.Executed)
	}

	return Result{}, nil
}

// ETSI TS 102 221 clause 11.1.1.3
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
