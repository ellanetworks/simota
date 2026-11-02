package rfm

import (
	"errors"
	"fmt"
)

// TS 31.102 clause 4.2.2
const FileIMSI uint16 = 0x6f07

// TS 31.102 clause 4.2.2
const IMSILength = 9

// TS 24.008 clause 10.5.1.4 table 10.5.4
const (
	typeIMSI  = 0x1
	oddDigits = 0x8
)

// TS 23.003 clause 2.2
func ValidIMSI(s string) bool {
	if len(s) < 6 || len(s) > 15 {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// TS 31.102 clause 4.2.2
// TS 24.008 clause 10.5.1.4
func EncodeIMSI(imsi string) ([]byte, error) {
	if !ValidIMSI(imsi) {
		return nil, fmt.Errorf("IMSI %q is not 6 to 15 digits", imsi)
	}

	nibbles := []byte{typeIMSI, imsi[0] - '0'}
	if len(imsi)%2 == 1 {
		nibbles[0] |= oddDigits
	}

	for i := 1; i < len(imsi); i++ {
		nibbles = append(nibbles, imsi[i]-'0')
	}

	if len(nibbles)%2 == 1 {
		nibbles = append(nibbles, 0xf)
	}

	ef := []byte{byte(len(nibbles) / 2)}
	for i := 0; i < len(nibbles); i += 2 {
		ef = append(ef, nibbles[i+1]<<4|nibbles[i])
	}

	for len(ef) < IMSILength {
		ef = append(ef, 0xff)
	}

	return ef, nil
}

func DecodeIMSI(ef []byte) (string, error) {
	if len(ef) != IMSILength {
		return "", fmt.Errorf("EF.IMSI of %d octets, not %d", len(ef), IMSILength)
	}

	n := int(ef[0])
	if n < 1 || n > IMSILength-1 {
		return "", fmt.Errorf("EF.IMSI length %d not in 1-%d", n, IMSILength-1)
	}

	if ef[1]&0x7 != typeIMSI {
		return "", errors.New("EF.IMSI does not hold an IMSI")
	}

	var digits []byte

	for i, o := range ef[1 : 1+n] {
		for j, d := range []byte{o & 0xf, o >> 4} {
			switch {
			case i == 0 && j == 0:
			case d <= 9:
				digits = append(digits, '0'+d)
			case d == 0xf && i == n-1 && j == 1:
			default:
				return "", fmt.Errorf("EF.IMSI holds a nibble %x that is not a digit", d)
			}
		}
	}

	if odd := ef[1]&oddDigits != 0; odd != (len(digits)%2 == 1) {
		return "", fmt.Errorf("EF.IMSI parity does not match its %d digits", len(digits))
	}

	imsi := string(digits)
	if !ValidIMSI(imsi) {
		return "", fmt.Errorf("EF.IMSI holds %q, not an IMSI of 6 to 15 digits", imsi)
	}

	return imsi, nil
}
