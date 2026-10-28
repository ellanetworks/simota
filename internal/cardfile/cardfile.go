// Package cardfile reads a card vendor's key file, such as sysmocom's: a header row, then a row per card, separated by
// tabs or commas.
package cardfile

import (
	"bytes"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Card is a card of a key file: only what OTA needs. Ki, OPc, PINs, ADM keys and KIKs are left out.
type Card struct {
	ICCID string
	// IMSI is empty when the file has none.
	IMSI    string
	KeySets []KeySet
}

// KeySet is the ciphering and integrity keys of a key set number.
type KeySet struct {
	Version  int
	KIc, KID []byte
}

var (
	keyColumn = regexp.MustCompile(`^(KIC|KID)([1-9]|1[0-5])$`)
	iccidRE   = regexp.MustCompile(`^[0-9]{18,20}$`)
	imsiRE    = regexp.MustCompile(`^[0-9]{6,15}$`)
)

var ErrFormat = errors.New("invalid key file")

// Parse reads the cards of a key file whose key sets use algorithm, "3des" or "aes": a key file does not tell it, and
// key lengths do not either (TS 102 225 clauses 5.1.2, 5.1.3). A card needs an ICCID and a key set with KIc and KID of
// the algorithm's length; a key set that lacks either is left out. The IMSI, informational, is read when given.
func Parse(r io.Reader, algorithm string) ([]Card, error) {
	lengths, ok := map[string][]int{"3des": {16}, "aes": {16, 24, 32}}[algorithm]
	if !ok {
		return nil, fmt.Errorf("unknown algorithm %q", algorithm)
	}

	b, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return nil, err
	}

	firstLine, _, _ := bytes.Cut(b, []byte("\n"))

	cr := csv.NewReader(bytes.NewReader(b))
	cr.Comma = ','

	if bytes.Contains(firstLine, []byte("\t")) {
		cr.Comma = '\t'
	}

	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("%w: no card after the header row", ErrFormat)
	}

	columns := map[string]int{}
	for i, name := range rows[0] {
		columns[strings.ToUpper(strings.TrimSpace(name))] = i
	}

	iccidCol, ok := columns["ICCID"]
	if !ok {
		return nil, fmt.Errorf("%w: the header needs an ICCID column", ErrFormat)
	}

	imsiCol, hasIMSI := columns["IMSI"]

	var cards []Card

	for n, row := range rows[1:] {
		line := n + 2

		if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
			continue
		}

		field := func(i int) string {
			if i < len(row) {
				return strings.TrimSpace(row[i])
			}

			return ""
		}

		c := Card{ICCID: NormalICCID(field(iccidCol))}
		if hasIMSI {
			c.IMSI = field(imsiCol)
		}

		if !iccidRE.MatchString(c.ICCID) {
			return nil, fmt.Errorf("%w: line %d: invalid ICCID %q", ErrFormat, line, c.ICCID)
		}

		if c.IMSI != "" && !imsiRE.MatchString(c.IMSI) {
			return nil, fmt.Errorf("%w: line %d: invalid IMSI %q", ErrFormat, line, c.IMSI)
		}

		sets := map[int]*KeySet{}

		for name, i := range columns {
			m := keyColumn.FindStringSubmatch(name)
			if m == nil || field(i) == "" {
				continue
			}

			key, err := hex.DecodeString(field(i))
			if err != nil {
				return nil, fmt.Errorf("%w: line %d: %s is not hexadecimal", ErrFormat, line, name)
			}

			v, _ := strconv.Atoi(m[2])
			if sets[v] == nil {
				sets[v] = &KeySet{Version: v}
			}

			if m[1] == "KIC" {
				sets[v].KIc = key
			} else {
				sets[v].KID = key
			}
		}

		for v := 1; v <= 15; v++ {
			s := sets[v]
			if s == nil {
				continue
			}

			if s.KIc == nil || s.KID == nil {
				continue
			}

			if len(s.KIc) != len(s.KID) || !slices.Contains(lengths, len(s.KIc)) {
				return nil, fmt.Errorf("%w: line %d: key set %d: keys of %d and %d octets for %s", ErrFormat, line, v,
					len(s.KIc), len(s.KID), algorithm)
			}

			c.KeySets = append(c.KeySets, *s)
		}

		if len(c.KeySets) == 0 {
			return nil, fmt.Errorf("%w: line %d: no key set with both KIC and KID", ErrFormat, line)
		}

		cards = append(cards, c)
	}

	return cards, nil
}

// NormalICCID is an ICCID without the 'F' that pads an odd number of digits, as some files print it (TS 102 221
// clause 13.2).
func NormalICCID(s string) string {
	if strings.HasSuffix(s, "F") || strings.HasSuffix(s, "f") {
		return s[:len(s)-1]
	}

	return s
}
