package rfm

import (
	"errors"
	"fmt"
	"slices"
)

// FileOPLMNwAcT is the identifier of EF.OPLMNwAcT under ADF.USIM (TS 31.102 clause 4.2.53).
const FileOPLMNwAcT uint16 = 0x6f61

// selectorEntryLength is the length of an entry of a PLMN selector with access technology: PLMN 3, access technology 2.
const selectorEntryLength = 5

// AccessTechnology is the Access Technology Identifier of a PLMN selector entry (TS 31.102 clause 4.2.5).
type AccessTechnology uint16

// The bits of the Access Technology Identifier (TS 31.102 clause 4.2.5). E-UTRAN and GSM are qualified by tables
// 4.2.5.1 and 4.2.5.2.
const (
	bitUTRAN             AccessTechnology = 0x8000
	bitEUTRAN            AccessTechnology = 0x4000
	bitEUTRANWB          AccessTechnology = 0x2000
	bitEUTRANNB          AccessTechnology = 0x1000
	bitNGRAN             AccessTechnology = 0x0800
	bitSatelliteNGRAN    AccessTechnology = 0x0400
	bitSatelliteEUTRANWB AccessTechnology = 0x0200
	bitSatelliteEUTRANNB AccessTechnology = 0x0100
	bitGSM               AccessTechnology = 0x0080
	bitGSMCompact        AccessTechnology = 0x0040
	bitCDMA2000HRPD      AccessTechnology = 0x0020
	bitCDMA20001xRTT     AccessTechnology = 0x0010
	bitECGSMIoT          AccessTechnology = 0x0008
	bitGSMQualifier      AccessTechnology = 0x0004
)

// The access technologies an entry selects, by name.
const (
	UTRAN               = "utran"
	EUTRANWBS1          = "eutran_wb_s1"
	EUTRANNBS1          = "eutran_nb_s1"
	NGRAN               = "ngran"
	SatelliteNGRAN      = "satellite_ngran"
	SatelliteEUTRANWBS1 = "satellite_eutran_wb_s1"
	SatelliteEUTRANNBS1 = "satellite_eutran_nb_s1"
	GSM                 = "gsm"
	ECGSMIoT            = "ec_gsm_iot"
	GSMCompact          = "gsm_compact"
	CDMA2000HRPD        = "cdma2000_hrpd"
	CDMA20001xRTT       = "cdma2000_1xrtt"
)

// AccessNames are the names of the access technologies, in the order of their bits.
var AccessNames = []string{
	UTRAN, EUTRANWBS1, EUTRANNBS1, NGRAN, SatelliteNGRAN, SatelliteEUTRANWBS1, SatelliteEUTRANNBS1, GSM, ECGSMIoT,
	GSMCompact, CDMA2000HRPD, CDMA20001xRTT,
}

var plainBits = map[string]AccessTechnology{
	UTRAN: bitUTRAN, NGRAN: bitNGRAN, SatelliteNGRAN: bitSatelliteNGRAN, SatelliteEUTRANWBS1: bitSatelliteEUTRANWB,
	SatelliteEUTRANNBS1: bitSatelliteEUTRANNB, GSMCompact: bitGSMCompact, CDMA2000HRPD: bitCDMA2000HRPD,
	CDMA20001xRTT: bitCDMA20001xRTT,
}

// Names are the access technologies that a selects (TS 31.102 clause 4.2.5, tables 4.2.5.1 and 4.2.5.2).
func (a AccessTechnology) Names() []string {
	sel := map[string]bool{}

	for name, bit := range plainBits {
		sel[name] = a&bit != 0
	}

	if a&bitEUTRAN != 0 {
		wb, nb := a&bitEUTRANWB != 0, a&bitEUTRANNB != 0
		sel[EUTRANWBS1], sel[EUTRANNBS1] = wb || !nb, nb || !wb
	}

	if a&bitGSM != 0 {
		ec, gsm := a&bitECGSMIoT != 0, a&bitGSMQualifier != 0
		sel[GSM], sel[ECGSMIoT] = gsm || !ec, ec || !gsm
	}

	var names []string

	for _, n := range AccessNames {
		if sel[n] {
			names = append(names, n)
		}
	}

	return names
}

// ParseAccess is the Access Technology Identifier that selects the named access technologies.
func ParseAccess(names []string) (AccessTechnology, error) {
	if len(names) == 0 {
		return 0, errors.New("no access technology")
	}

	sel := map[string]bool{}

	for _, n := range names {
		if !slices.Contains(AccessNames, n) {
			return 0, fmt.Errorf("unknown access technology %q", n)
		}

		sel[n] = true
	}

	var a AccessTechnology

	for name, bit := range plainBits {
		if sel[name] {
			a |= bit
		}
	}

	switch {
	case sel[EUTRANWBS1] && sel[EUTRANNBS1]:
		a |= bitEUTRAN
	case sel[EUTRANWBS1]:
		a |= bitEUTRAN | bitEUTRANWB
	case sel[EUTRANNBS1]:
		a |= bitEUTRAN | bitEUTRANNB
	}

	switch {
	case sel[GSM] && sel[ECGSMIoT]:
		a |= bitGSM
	case sel[GSM]:
		a |= bitGSM | bitGSMQualifier
	case sel[ECGSMIoT]:
		a |= bitGSM | bitECGSMIoT
	}

	return a, nil
}

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
