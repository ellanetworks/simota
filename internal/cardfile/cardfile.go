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
	"strconv"
	"strings"
)

// Card is a card of a key file: only what OTA needs. Ki, OPc, PINs and ADM keys are left out.
type Card struct {
	ICCID   string
	IMSI    string
	KeySets []KeySet
}

// KeySet is the keys of a key set number. A key file does not tell the algorithm: 16-octet keys are taken as triple
// DES, the sysmoISIM's, and longer ones as AES.
type KeySet struct {
	Version   int
	Algorithm string
	KIc, KID  []byte
	KIK       []byte
}

var (
	keyColumn = regexp.MustCompile(`^(KIC|KID|KIK)([1-9]|1[0-5])$`)
	iccidRE   = regexp.MustCompile(`^[0-9]{18,20}$`)
	imsiRE    = regexp.MustCompile(`^[0-9]{6,15}$`)
)

var ErrFormat = errors.New("invalid key file")

// Parse reads the cards of a key file. A card needs an ICCID, an IMSI, and a key set with KIc and KID; a key set that
// lacks either is left out.
func Parse(r io.Reader) ([]Card, error) {
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

	iccidCol, ok1 := columns["ICCID"]
	imsiCol, ok2 := columns["IMSI"]

	if !ok1 || !ok2 {
		return nil, fmt.Errorf("%w: the header needs ICCID and IMSI columns", ErrFormat)
	}

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

		c := Card{ICCID: field(iccidCol), IMSI: field(imsiCol)}
		if !iccidRE.MatchString(c.ICCID) {
			return nil, fmt.Errorf("%w: line %d: invalid ICCID %q", ErrFormat, line, c.ICCID)
		}

		if !imsiRE.MatchString(c.IMSI) {
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

			switch m[1] {
			case "KIC":
				sets[v].KIc = key
			case "KID":
				sets[v].KID = key
			case "KIK":
				sets[v].KIK = key
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

			alg, err := algorithm(s.KIc, s.KID, s.KIK)
			if err != nil {
				return nil, fmt.Errorf("%w: line %d: key set %d: %v", ErrFormat, line, v, err)
			}

			s.Algorithm = alg
			c.KeySets = append(c.KeySets, *s)
		}

		if len(c.KeySets) == 0 {
			return nil, fmt.Errorf("%w: line %d: no key set with both KIC and KID", ErrFormat, line)
		}

		cards = append(cards, c)
	}

	return cards, nil
}

func algorithm(keys ...[]byte) (string, error) {
	n := len(keys[0])

	for _, k := range keys {
		if k != nil && len(k) != n {
			return "", errors.New("keys of different lengths")
		}
	}

	switch n {
	case 16:
		return "3des", nil
	case 24, 32:
		return "aes", nil
	default:
		return "", fmt.Errorf("keys of %d octets", n)
	}
}
