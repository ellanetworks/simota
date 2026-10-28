package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	"github.com/ellanetworks/simota/internal/cardfile"
	"github.com/ellanetworks/simota/internal/db"
)

// defaultTAR is the first TAR of the ADF RFM applications in the compact format (TS 101 220 table D.1).
var defaultTAR = []byte{0xb0, 0x00, 0x01}

var (
	iccidRE  = regexp.MustCompile(`^[0-9]{18,20}$`)
	imsiRE   = regexp.MustCompile(`^[0-9]{6,15}$`)
	msisdnRE = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)
)

// Card is a card without its keys: the API never returns key material.
type Card struct {
	ICCID       string         `json:"iccid"`
	IMSI        string         `json:"imsi"`
	MSISDN      string         `json:"msisdn"`
	Description string         `json:"description"`
	TAR         string         `json:"tar"`
	KVN         int            `json:"kvn"`
	KeySets     []KeySetStatus `json:"key_sets"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// KeySetStatus is a key set without its keys.
type KeySetStatus struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	CCLength  int    `json:"cc_length"`
	// Counter is the last CNTR sent with the key set.
	Counter uint64 `json:"counter"`
}

type ListCardsResponse struct {
	Items      []Card `json:"items"`
	Page       int    `json:"page"`
	PerPage    int    `json:"per_page"`
	TotalCount int    `json:"total_count"`
}

type CreateCardParams struct {
	ICCID       string         `json:"iccid"`
	IMSI        string         `json:"imsi"`
	MSISDN      string         `json:"msisdn"`
	Description string         `json:"description"`
	TAR         string         `json:"tar"`
	KVN         int            `json:"kvn"`
	KeySets     []KeySetParams `json:"key_sets"`
}

type KeySetParams struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	KIc       string `json:"kic"`
	KID       string `json:"kid"`
	// CCLength is that of an AES key set's checksums, 4 or 8; 0 is 8.
	CCLength int `json:"cc_length"`
}

type UpdateCardParams struct {
	IMSI        string `json:"imsi"`
	MSISDN      string `json:"msisdn"`
	Description string `json:"description"`
	TAR         string `json:"tar"`
	KVN         int    `json:"kvn"`
}

type ImportCardsResponse struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
}

func ListCards(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		cards, total, err := cfg.Store.ListCards(r.Context(), page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list cards", err, cfg.Logger)
			return
		}

		items := make([]Card, 0, len(cards))
		for _, c := range cards {
			items = append(items, cardResponse(c))
		}

		writeResponse(w, ListCardsResponse{Items: items, Page: page, PerPage: perPage, TotalCount: total}, http.StatusOK, cfg.Logger)
	})
}

func GetCard(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := cfg.Store.GetCard(r.Context(), cardfile.NormalICCID(r.PathValue("iccid")))
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Card not found", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get card", err, cfg.Logger)
			return
		}

		writeResponse(w, cardResponse(c), http.StatusOK, cfg.Logger)
	})
}

func CreateCard(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p CreateCardParams
		if !decodeBody(w, r, &p, cfg) {
			return
		}

		c, err := p.card()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
			return
		}

		err = cfg.Store.CreateCard(r.Context(), c)
		if errors.Is(err, db.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "A card with this ICCID already exists", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to create card", err, cfg.Logger)
			return
		}

		created, err := cfg.Store.GetCard(r.Context(), c.ICCID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get card", err, cfg.Logger)
			return
		}

		writeResponse(w, cardResponse(created), http.StatusCreated, cfg.Logger)
	})
}

func UpdateCard(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p UpdateCardParams
		if !decodeBody(w, r, &p, cfg) {
			return
		}

		c, err := cfg.Store.GetCard(r.Context(), cardfile.NormalICCID(r.PathValue("iccid")))
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Card not found", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get card", err, cfg.Logger)
			return
		}

		c.IMSI, c.MSISDN, c.Description, c.KVN = p.IMSI, p.MSISDN, p.Description, p.KVN

		if c.TAR, err = parseTAR(p.TAR); err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
			return
		}

		if err := validate(c); err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
			return
		}

		if err := cfg.Store.UpdateCard(r.Context(), c); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to update card", err, cfg.Logger)
			return
		}

		updated, err := cfg.Store.GetCard(r.Context(), c.ICCID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get card", err, cfg.Logger)
			return
		}

		writeResponse(w, cardResponse(updated), http.StatusOK, cfg.Logger)
	})
}

func DeleteCard(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := cfg.Store.DeleteCard(r.Context(), cardfile.NormalICCID(r.PathValue("iccid")))
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Card not found", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to delete card", err, cfg.Logger)
			return
		}

		writeResponse(w, SuccessResponse{Message: "Card deleted successfully"}, http.StatusOK, cfg.Logger)
	})
}

// ImportCards stores the cards of a vendor's key file, sent as the request body. The algorithm query parameter, 3des
// or aes, tells the key sets' algorithm, and cc_length, 4 or 8, the length of an AES key set's checksums.
func ImportCards(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		algorithm := q.Get("algorithm")
		if algorithm != "3des" && algorithm != "aes" {
			writeError(w, http.StatusBadRequest, "algorithm must be 3des or aes", nil, cfg.Logger)
			return
		}

		ccLength, ok := atoiDefault(q.Get("cc_length"), 8)
		if !ok || (ccLength != 8 && (algorithm != "aes" || ccLength != 4)) {
			writeError(w, http.StatusBadRequest, "cc_length must be 8, or 4 for aes", nil, cfg.Logger)
			return
		}

		parsed, err := cardfile.Parse(http.MaxBytesReader(w, r.Body, maxImportBody), algorithm)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
			return
		}

		cards := make([]db.Card, 0, len(parsed))

		for _, p := range parsed {
			c := db.Card{ICCID: p.ICCID, IMSI: p.IMSI, TAR: defaultTAR, KVN: p.KeySets[0].Version}
			for _, k := range p.KeySets {
				c.KeySets = append(c.KeySets, db.KeySet{Version: k.Version, Algorithm: algorithm, KIc: k.KIc, KID: k.KID, CCLength: ccLength})
			}

			cards = append(cards, c)
		}

		created, updated, err := cfg.Store.ImportCards(r.Context(), cards)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to import cards", err, cfg.Logger)
			return
		}

		writeResponse(w, ImportCardsResponse{Created: created, Updated: updated}, http.StatusOK, cfg.Logger)
	})
}

func (p CreateCardParams) card() (db.Card, error) {
	c := db.Card{ICCID: cardfile.NormalICCID(p.ICCID), IMSI: p.IMSI, MSISDN: p.MSISDN, Description: p.Description, KVN: p.KVN}

	var err error
	if c.TAR, err = parseTAR(p.TAR); err != nil {
		return db.Card{}, err
	}

	if len(p.KeySets) == 0 {
		return db.Card{}, errors.New("a card needs a key set")
	}

	for _, k := range p.KeySets {
		ks, err := k.keySet()
		if err != nil {
			return db.Card{}, err
		}

		if slices.ContainsFunc(c.KeySets, func(o db.KeySet) bool { return o.Version == ks.Version }) {
			return db.Card{}, fmt.Errorf("key set %d is given twice", ks.Version)
		}

		c.KeySets = append(c.KeySets, ks)
	}

	if c.KVN == 0 {
		c.KVN = c.KeySets[0].Version
	}

	return c, validate(c)
}

func (p KeySetParams) keySet() (db.KeySet, error) {
	k := db.KeySet{Version: p.Version, Algorithm: p.Algorithm, CCLength: p.CCLength}
	if k.Version < 1 || k.Version > 15 {
		return db.KeySet{}, fmt.Errorf("key set version %d not in 1-15", k.Version)
	}

	if k.CCLength == 0 {
		k.CCLength = 8
	}

	if k.CCLength != 8 && (k.Algorithm != "aes" || k.CCLength != 4) {
		return db.KeySet{}, fmt.Errorf("key set %d: cc_length must be 8, or 4 for aes", k.Version)
	}

	var lengths []int

	switch k.Algorithm {
	case "3des":
		lengths = []int{16}
	case "aes":
		lengths = []int{16, 24, 32}
	default:
		return db.KeySet{}, fmt.Errorf("unknown algorithm %q", k.Algorithm)
	}

	key := func(name, s string) ([]byte, error) {
		b, err := hex.DecodeString(s)
		if err != nil || !slices.Contains(lengths, len(b)) {
			return nil, fmt.Errorf("key set %d: %s must be %v octets of hexadecimal", k.Version, name, lengths)
		}

		return b, nil
	}

	var err error
	if k.KIc, err = key("KIc", p.KIc); err != nil {
		return db.KeySet{}, err
	}

	if k.KID, err = key("KID", p.KID); err != nil {
		return db.KeySet{}, err
	}

	return k, nil
}

func validate(c db.Card) error {
	switch {
	case !iccidRE.MatchString(c.ICCID):
		return errors.New("ICCID must be 18 to 20 digits")
	case !imsiRE.MatchString(c.IMSI):
		return errors.New("IMSI must be 6 to 15 digits")
	case c.MSISDN != "" && !msisdnRE.MatchString(c.MSISDN):
		return errors.New("MSISDN must be E.164, such as +15550000001")
	case !slices.ContainsFunc(c.KeySets, func(k db.KeySet) bool { return k.Version == c.KVN }):
		return fmt.Errorf("the card has no key set %d", c.KVN)
	}

	return nil
}

// parseTAR is the TAR of the RFM application whose current directory is ADF.USIM at the start of a script (TS 102 226
// clause 7.3): one of the ADF range, 'B0 00 01' or 'B0 00 20' to 'B0 01 1F' (TS 101 220 table D.1), or of the SIM
// File System range, 'B0 00 10' to 'B0 00 1F', where some cards put it, such as the sysmoISIM-SJA5 at 'B0 00 11'. A
// TAR that leads elsewhere fails a job's first SELECT.
func parseTAR(s string) ([]byte, error) {
	if s == "" {
		return defaultTAR, nil
	}

	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 3 {
		return nil, errors.New("TAR must be 3 octets of hexadecimal, such as b00001")
	}

	if n := int(b[0])<<16 | int(b[1])<<8 | int(b[2]); n != 0xb00001 && (n < 0xb00010 || n > 0xb0011f) {
		return nil, errors.New("TAR must be that of an RFM application reaching ADF.USIM: b00001, or b00010 to b0011f")
	}

	return b, nil
}

func cardResponse(c db.Card) Card {
	out := Card{
		ICCID: c.ICCID, IMSI: c.IMSI, MSISDN: c.MSISDN, Description: c.Description, TAR: hex.EncodeToString(c.TAR),
		KVN: c.KVN, KeySets: make([]KeySetStatus, 0, len(c.KeySets)),
		CreatedAt: formatTime(c.CreatedAt), UpdatedAt: formatTime(c.UpdatedAt),
	}

	for _, k := range c.KeySets {
		out.KeySets = append(out.KeySets, KeySetStatus{Version: k.Version, Algorithm: k.Algorithm, CCLength: k.CCLength, Counter: k.Counter})
	}

	return out
}

type KeySetUpdateParams struct {
	// Counter is the last counter the card accepted with the key set; the next command uses one more.
	Counter *uint64 `json:"counter"`
}

// UpdateKeySet sets the counter of a card's key set, for a card used before by another sending entity.
func UpdateKeySet(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p KeySetUpdateParams
		if !decodeBody(w, r, &p, cfg) {
			return
		}

		version, err := strconv.Atoi(r.PathValue("version"))
		if err != nil || version < 1 || version > 15 {
			writeError(w, http.StatusBadRequest, "Invalid key set version", err, cfg.Logger)
			return
		}

		if p.Counter == nil || *p.Counter > maxCounter {
			writeError(w, http.StatusBadRequest, "counter must be between 0 and 1099511627775", nil, cfg.Logger)
			return
		}

		iccid := cardfile.NormalICCID(r.PathValue("iccid"))

		err = cfg.Store.SetCounter(r.Context(), iccid, version, *p.Counter)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Key set not found", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to update the key set", err, cfg.Logger)
			return
		}

		c, err := cfg.Store.GetCard(r.Context(), iccid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get card", err, cfg.Logger)
			return
		}

		writeResponse(w, cardResponse(c), http.StatusOK, cfg.Logger)
	})
}

// maxCounter is the largest CNTR, 5 octets (TS 102 225 clause 5.1).
const maxCounter = 1<<40 - 1
