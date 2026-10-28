package rfm

import (
	"fmt"
	"strings"
)

// File identifiers of the PLMN selector of the operator (TS 31.102 clause 4.2.53, TS 51.011 clause 10.3.35).
const (
	FileDFGSM     uint16 = 0x7f20
	FileOPLMNwAcT uint16 = 0x6f61
)

// selectorEntryLength is the length of an entry of a PLMN selector with access technology: PLMN 3, access technology 2.
const selectorEntryLength = 5

// AccessTechnology is the Access Technology Identifier of a PLMN selector entry (TS 31.102 clause 4.2.5).
type AccessTechnology uint16

const (
	UTRAN AccessTechnology = 0x8000
	// EUTRAN is E-UTRAN in WB-S1 mode and NB-S1 mode (TS 31.102 table 4.2.5.1).
	EUTRAN AccessTechnology = 0x4000
	NGRAN  AccessTechnology = 0x0800
	// GSM is GSM and EC-GSM-IoT (TS 31.102 table 4.2.5.2).
	GSM AccessTechnology = 0x0080
)

// SelectorEntry is an entry of a PLMN selector with access technology.
type SelectorEntry struct {
	MCC, MNC string
	Access   AccessTechnology
}

// Empty is the entry of an unused slot.
var Empty = SelectorEntry{}

// Encode codes e as its 5 octets: the PLMN as in TS 24.008 clause 10.5.1.3, then the access technology. An empty entry
// is 'FFFFFF0000'.
func (e SelectorEntry) Encode() ([]byte, error) {
	if e == Empty {
		return []byte{0xff, 0xff, 0xff, 0x00, 0x00}, nil
	}

	if !digits(e.MCC, 3) || !digits(e.MNC, 2) {
		return nil, fmt.Errorf("invalid PLMN %s/%s", e.MCC, e.MNC)
	}

	d := func(s string, i int) byte { return s[i] - '0' }

	mnc3 := byte(0xf)
	if len(e.MNC) == 3 {
		mnc3 = d(e.MNC, 2)
	}

	return []byte{
		d(e.MCC, 1)<<4 | d(e.MCC, 0),
		mnc3<<4 | d(e.MCC, 2),
		d(e.MNC, 1)<<4 | d(e.MNC, 0),
		byte(e.Access >> 8), byte(e.Access),
	}, nil
}

// DecodeSelector decodes the entries of a PLMN selector with access technology. Unused slots are Empty.
func DecodeSelector(b []byte) ([]SelectorEntry, error) {
	if len(b)%selectorEntryLength != 0 {
		return nil, fmt.Errorf("selector of %d octets is not a multiple of %d", len(b), selectorEntryLength)
	}

	out := make([]SelectorEntry, 0, len(b)/selectorEntryLength)

	for i := 0; i < len(b); i += selectorEntryLength {
		p := b[i : i+selectorEntryLength]
		if p[0] == 0xff && p[1] == 0xff && p[2] == 0xff {
			out = append(out, Empty)
			continue
		}

		mcc := []byte{'0' + p[0]&0xf, '0' + p[0]>>4, '0' + p[1]&0xf}
		mnc := []byte{'0' + p[2]&0xf, '0' + p[2]>>4}

		if p[1]>>4 != 0xf {
			mnc = append(mnc, '0'+p[1]>>4)
		}

		e := SelectorEntry{MCC: string(mcc), MNC: string(mnc), Access: AccessTechnology(p[3])<<8 | AccessTechnology(p[4])}
		if !digits(e.MCC, 3) || !digits(e.MNC, 2) {
			return nil, fmt.Errorf("entry %d: invalid PLMN %x", i/selectorEntryLength+1, p[:3])
		}

		out = append(out, e)
	}

	return out, nil
}

// SlotOffset is the offset of slot n, from 1, of a PLMN selector.
func SlotOffset(n int) uint16 {
	return uint16((n - 1) * selectorEntryLength)
}

func (a AccessTechnology) String() string {
	var names []string

	for _, t := range []struct {
		bit  AccessTechnology
		name string
	}{{UTRAN, "UTRAN"}, {EUTRAN, "E-UTRAN"}, {NGRAN, "NG-RAN"}, {GSM, "GSM"}} {
		if a&t.bit != 0 {
			names = append(names, t.name)
		}
	}

	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ", ")
}

// digits reports whether s is lo to 3 decimal digits, as an MCC or MNC is.
func digits(s string, lo int) bool {
	if len(s) < lo || len(s) > 3 {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}
