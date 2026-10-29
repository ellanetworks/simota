package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/jobs"
	"github.com/ellanetworks/simota/internal/ota"
	"github.com/ellanetworks/simota/internal/rfm"
)

// maxJobCards bounds the cards of one request.
const maxJobCards = 100

var (
	mccRE = regexp.MustCompile(`^[0-9]{3}$`)
	mncRE = regexp.MustCompile(`^[0-9]{2,3}$`)
)

type CreateJobsParams struct {
	ICCIDs    []string `json:"iccids"`
	Operation string   `json:"operation"`
	// Slot is the entry, from 1, of a set or a clear; MCC, MNC and Access are the entry of a set.
	Slot   int      `json:"slot"`
	MCC    string   `json:"mcc"`
	MNC    string   `json:"mnc"`
	Access []string `json:"access"`
	// IMSI is the IMSI an IMSI write writes, and PreviousIMSI the IMSI it replaces, which must be the card's IMSI on
	// record: the operator states which subscription the card leaves.
	IMSI         string `json:"imsi"`
	PreviousIMSI string `json:"previous_imsi"`
}

type Job struct {
	ID        int64    `json:"id"`
	ICCID     string   `json:"iccid"`
	Operation string   `json:"operation"`
	Slot      int      `json:"slot,omitempty"`
	MCC       string   `json:"mcc,omitempty"`
	MNC       string   `json:"mnc,omitempty"`
	Access    []string `json:"access,omitempty"`
	// AccessRaw is the Access Technology Identifier of a set, in hexadecimal.
	AccessRaw string `json:"access_raw,omitempty"`
	// IMSI and PreviousIMSI are those of an IMSI write; IMSIReadBack is the IMSI that EF.IMSI read back after it.
	IMSI         string `json:"imsi,omitempty"`
	PreviousIMSI string `json:"previous_imsi,omitempty"`
	IMSIReadBack string `json:"imsi_read_back,omitempty"`
	// TAR and KVN are those the job's commands use.
	TAR    string `json:"tar"`
	KVN    int    `json:"kvn"`
	Status string `json:"status"`
	// Step is the number of commands answered, and FileSize the size of EF.OPLMNwAcT once read.
	Step      int    `json:"step"`
	FileSize  *int   `json:"file_size,omitempty"`
	Counter   uint64 `json:"counter,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	// PoRStatus is the Response Status Code of the card's PoR, such as por_ok.
	PoRStatus string `json:"por_status,omitempty"`
	// StatusWord is that of the script's last command, in hexadecimal; Warning is one that completed with a warning.
	StatusWord string `json:"status_word,omitempty"`
	Warning    bool   `json:"warning,omitempty"`
	// Entries are EF.OPLMNwAcT as the card read it after the job.
	Entries []Entry `json:"entries,omitempty"`
	// Command and Response are the user data headers and user data of the secured packets, in hexadecimal.
	Command   string `json:"command,omitempty"`
	Response  string `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Entry is an entry of EF.OPLMNwAcT. An unused one has no MCC.
type Entry struct {
	Slot      int      `json:"slot"`
	MCC       string   `json:"mcc,omitempty"`
	MNC       string   `json:"mnc,omitempty"`
	Access    []string `json:"access,omitempty"`
	AccessRaw string   `json:"access_raw,omitempty"`
}

type JobsResponse struct {
	Items []Job `json:"items"`
}

type ListJobsResponse struct {
	Items      []Job `json:"items"`
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	TotalCount int   `json:"total_count"`
}

// CreateJobs queues a job per card.
func CreateJobs(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p CreateJobsParams
		if !decodeBody(w, r, &p, cfg) {
			return
		}

		template, err := p.job()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
			return
		}

		if _, err := cfg.Store.GetSMSC(r.Context()); errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusConflict, "Set the SMSC before creating jobs", err, cfg.Logger)
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get the SMSC", err, cfg.Logger)
			return
		}

		if len(p.ICCIDs) == 0 || len(p.ICCIDs) > maxJobCards {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("iccids must hold 1 to %d cards", maxJobCards), nil, cfg.Logger)
			return
		}

		// An IMSI is a single subscription's: it is written to one card.
		if template.Operation == db.OperationIMSIWrite && len(p.ICCIDs) != 1 {
			writeError(w, http.StatusBadRequest, "An IMSI write takes one card", nil, cfg.Logger)
			return
		}

		batch := make([]db.Job, 0, len(p.ICCIDs))
		seen := map[string]bool{}

		for _, iccid := range p.ICCIDs {
			if seen[iccid] {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("Card %s is given twice", iccid), nil, cfg.Logger)
				return
			}

			seen[iccid] = true

			c, err := cfg.Store.GetCard(r.Context(), iccid)
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("Card %s not found", iccid), err, cfg.Logger)
				return
			}

			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to get card", err, cfg.Logger)
				return
			}

			if c.MSISDN == "" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("Card %s has no MSISDN", iccid), nil, cfg.Logger)
				return
			}

			if template.Operation == db.OperationIMSIWrite && c.IMSI == "" {
				writeError(w, http.StatusConflict, fmt.Sprintf("Card %s has no IMSI on record: set the IMSI it holds first",
					iccid), nil, cfg.Logger)

				return
			}

			j := template
			j.ICCID = iccid
			batch = append(batch, j)
		}

		ids, err := cfg.Store.CreateJobs(r.Context(), batch)
		if errors.Is(err, db.ErrBusy) {
			writeError(w, http.StatusConflict, "A card has a job in progress", err, cfg.Logger)
			return
		}

		if errors.Is(err, db.ErrIMSIMismatch) {
			writeError(w, http.StatusConflict, "previous_imsi is not the card's IMSI on record", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to create jobs", err, cfg.Logger)
			return
		}

		resp := JobsResponse{Items: make([]Job, 0, len(ids))}

		for _, id := range ids {
			j, err := cfg.Store.GetJob(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to get job", err, cfg.Logger)
				return
			}

			resp.Items = append(resp.Items, jobResponse(j))
		}

		if cfg.Notify != nil {
			cfg.Notify()
		}

		writeResponse(w, resp, http.StatusCreated, cfg.Logger)
	})
}

func ListJobs(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		list, total, err := cfg.Store.ListJobs(r.Context(), r.URL.Query().Get("search"), page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list jobs", err, cfg.Logger)
			return
		}

		resp := ListJobsResponse{Items: make([]Job, 0, len(list)), Page: page, PerPage: perPage, TotalCount: total}
		for _, j := range list {
			resp.Items = append(resp.Items, jobResponse(j))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetJob(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid job ID", err, cfg.Logger)
			return
		}

		j, err := cfg.Store.GetJob(r.Context(), id)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Job not found", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get job", err, cfg.Logger)
			return
		}

		writeResponse(w, jobResponse(j), http.StatusOK, cfg.Logger)
	})
}

func (p CreateJobsParams) job() (db.Job, error) {
	j := db.Job{Operation: db.Operation(p.Operation)}

	if j.Operation != db.OperationIMSIWrite && (p.IMSI != "" || p.PreviousIMSI != "") {
		return db.Job{}, errors.New("only an IMSI write takes imsi and previous_imsi")
	}

	switch j.Operation {
	case db.OperationOPLMNRead:
		if p.Slot != 0 || p.MCC != "" || p.MNC != "" || len(p.Access) > 0 {
			return db.Job{}, errors.New("a read takes no slot, mcc, mnc or access")
		}

		return j, nil
	case db.OperationIMSIWrite:
		if p.Slot != 0 || p.MCC != "" || p.MNC != "" || len(p.Access) > 0 {
			return db.Job{}, errors.New("an IMSI write takes no slot, mcc, mnc or access")
		}

		if !rfm.ValidIMSI(p.IMSI) || !rfm.ValidIMSI(p.PreviousIMSI) {
			return db.Job{}, errors.New("imsi and previous_imsi must be 6 to 15 digits")
		}

		j.IMSI, j.PreviousIMSI = p.IMSI, p.PreviousIMSI

		return j, nil
	case db.OperationOPLMNClear:
		if p.MCC != "" || p.MNC != "" || len(p.Access) > 0 {
			return db.Job{}, errors.New("a clear takes no mcc, mnc or access")
		}
	case db.OperationOPLMNSet:
		if !mccRE.MatchString(p.MCC) || !mncRE.MatchString(p.MNC) {
			return db.Job{}, errors.New("mcc must be 3 digits and mnc 2 or 3 digits")
		}

		access, err := rfm.ParseAccess(p.Access)
		if err != nil {
			return db.Job{}, fmt.Errorf("access: %w", err)
		}

		j.MCC, j.MNC, j.Access = p.MCC, p.MNC, int(access)
	default:
		return db.Job{}, errors.New("operation must be oplmn_read, oplmn_set, oplmn_clear or imsi_write")
	}

	if p.Slot < 1 || p.Slot > jobs.MaxSlot {
		return db.Job{}, fmt.Errorf("slot must be 1 to %d", jobs.MaxSlot)
	}

	j.Slot = p.Slot

	return j, nil
}

func jobResponse(j db.Job) Job {
	out := Job{
		ID: j.ID, ICCID: j.ICCID, Operation: string(j.Operation), Slot: j.Slot, MCC: j.MCC, MNC: j.MNC,
		IMSI: j.IMSI, PreviousIMSI: j.PreviousIMSI, TAR: hex.EncodeToString(j.TAR), KVN: j.KVN, Status: string(j.Status), Step: j.Step, Counter: j.Counter,
		MessageID: j.MessageID, Warning: j.Warning, Command: hex.EncodeToString(j.Command),
		Response: hex.EncodeToString(j.Response), Error: j.Error, CreatedAt: formatTime(j.CreatedAt),
		UpdatedAt: formatTime(j.UpdatedAt),
	}

	if j.Operation == db.OperationOPLMNSet {
		a := rfm.AccessTechnology(j.Access)
		out.Access, out.AccessRaw = a.Names(), fmt.Sprintf("%04x", j.Access)
	}

	if j.FileSize >= 0 {
		size := j.FileSize
		out.FileSize = &size
	}

	if j.PoRStatus >= 0 {
		out.PoRStatus = ota.Status(j.PoRStatus).String()
	}

	if j.StatusWord >= 0 {
		out.StatusWord = fmt.Sprintf("%04x", j.StatusWord)
	}

	switch {
	case j.Operation == db.OperationIMSIWrite:
		// A read-back that differs from the IMSI written is shown too: it is what the card holds.
		out.IMSIReadBack, _ = rfm.DecodeIMSI(j.Data)
	case j.Status == db.JobSucceeded || j.Status == db.JobUnconfirmed:
		out.Entries = entriesOf(j)
	}

	return out
}

// entriesOf are the entries a job read: the whole file for a read, the slot read back for a set or a clear.
func entriesOf(j db.Job) []Entry {
	entries, err := rfm.DecodeSelector(j.Data)
	if err != nil {
		return nil
	}

	first := 1
	if j.Operation != db.OperationOPLMNRead {
		first = j.Slot
	}

	out := make([]Entry, 0, len(entries))

	for i, e := range entries {
		entry := Entry{Slot: first + i, MCC: e.MCC, MNC: e.MNC}
		if e != rfm.Empty {
			entry.Access, entry.AccessRaw = e.Access.Names(), fmt.Sprintf("%04x", uint16(e.Access))
		}

		out = append(out, entry)
	}

	return out
}
