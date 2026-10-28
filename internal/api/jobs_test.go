package api_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/api"
	"github.com/ellanetworks/simota/internal/db"
)

func createCard(t *testing.T, e env, iccid, msisdn string) {
	t.Helper()

	setSMSC(t, e)

	body := strings.Replace(strings.Replace(cardBody(), "8988211000000000017", iccid, 1), `"+15550000001"`, `"`+msisdn+`"`, 1)
	if code, b := e.do(t, http.MethodPost, "/api/v1/cards", "application/json", body); code != http.StatusCreated {
		t.Fatalf("create card = %d %s", code, b)
	}
}

func TestCreateJobs(t *testing.T) {
	e := newEnv(t)
	createCard(t, e, "8988211000000000017", "+15550000001")
	createCard(t, e, "8988211000000000025", "+15550000002")

	code, body := e.do(t, http.MethodPost, "/api/v1/jobs", "application/json",
		`{"iccids":["8988211000000000017","8988211000000000025"],"operation":"oplmn_set","slot":2,"mcc":"999","mnc":"99","access":["eutran_wb_s1","eutran_nb_s1","ngran"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}

	var created struct{ Result api.JobsResponse }
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	if len(created.Result.Items) != 2 {
		t.Fatalf("jobs = %+v", created.Result.Items)
	}

	j := created.Result.Items[0]
	if j.ICCID != "8988211000000000017" || j.Operation != "oplmn_set" || j.Slot != 2 || j.MCC != "999" || j.MNC != "99" ||
		strings.Join(j.Access, ",") != "eutran_wb_s1,eutran_nb_s1,ngran" || j.AccessRaw != "4800" || j.Status != "queued" ||
		j.TAR != "b00001" || j.KVN != 1 || j.Step != 0 || j.FileSize != nil {
		t.Fatalf("job = %+v", j)
	}

	// Once for the SMSC, once for the jobs.
	if *e.notified != 2 {
		t.Fatalf("runner notified %d times", *e.notified)
	}

	// The card already has a job in progress.
	if code, _ := e.do(t, http.MethodPost, "/api/v1/jobs", "application/json",
		`{"iccids":["8988211000000000017"],"operation":"oplmn_read"}`); code != http.StatusConflict {
		t.Fatalf("second job = %d, want 409", code)
	}

	code, body = e.do(t, http.MethodGet, "/api/v1/jobs?iccid=8988211000000000025", "", "")

	var list struct{ Result api.ListJobsResponse }
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}

	if code != http.StatusOK || list.Result.TotalCount != 1 || list.Result.Items[0].ICCID != "8988211000000000025" {
		t.Fatalf("list = %d %s", code, body)
	}

	if _, body := e.do(t, http.MethodGet, "/api/v1/jobs", "", ""); !strings.Contains(body, `"total_count":2`) {
		t.Fatalf("list all = %s", body)
	}

	if code, body := e.do(t, http.MethodGet, "/api/v1/jobs/"+itoa(j.ID), "", ""); code != http.StatusOK || !strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("get = %d %s", code, body)
	}
}

func TestCreateJobsRejects(t *testing.T) {
	e := newEnv(t)
	createCard(t, e, "8988211000000000017", "+15550000001")
	createCard(t, e, "8988211000000000025", "")

	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"iccids":[],"operation":"oplmn_read"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_delete"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_read","slot":2}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_clear"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_clear","slot":52}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_clear","slot":2,"mcc":"999"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_set","slot":2,"mcc":"99","mnc":"99","access":["eutran_wb_s1"]}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_set","slot":2,"mcc":"999","mnc":"99","access":[]}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_set","slot":2,"mcc":"999","mnc":"99","access":["eutran"]}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017","8988211000000000017"],"operation":"oplmn_read"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000033"],"operation":"oplmn_read"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000025"],"operation":"oplmn_read"}`, http.StatusBadRequest},
		{`{"iccids":["8988211000000000017"],"operation":"oplmn_read","priority":1}`, http.StatusBadRequest},
	} {
		if code, body := e.do(t, http.MethodPost, "/api/v1/jobs", "application/json", tc.body); code != tc.code {
			t.Fatalf("%s = %d %s, want %d", tc.body, code, body, tc.code)
		}
	}

	if _, body := e.do(t, http.MethodGet, "/api/v1/jobs", "", ""); !strings.Contains(body, `"total_count":0`) {
		t.Fatalf("a refused request queued jobs: %s", body)
	}

	if code, _ := e.do(t, http.MethodGet, "/api/v1/jobs/x", "", ""); code != http.StatusBadRequest {
		t.Fatalf("get x = %d", code)
	}

	if code, _ := e.do(t, http.MethodGet, "/api/v1/jobs/9", "", ""); code != http.StatusNotFound {
		t.Fatalf("get 9 = %d", code)
	}
}

// TestJobEntries checks the entries of finished jobs: the file a read read, the slot a set read back.
func TestJobEntries(t *testing.T) {
	e := newEnv(t)
	createCard(t, e, "8988211000000000017", "+15550000001")

	finish := func(body string, status db.JobStatus, data string) api.Job {
		t.Helper()

		_, b := e.do(t, http.MethodPost, "/api/v1/jobs", "application/json", body)

		var created struct{ Result api.JobsResponse }
		if err := json.Unmarshal([]byte(b), &created); err != nil {
			t.Fatal(err)
		}

		id := created.Result.Items[0].ID
		j, _ := e.store.GetJob(context.Background(), id)

		if err := e.store.MarkJobSent(context.Background(), id, 1, "9", []byte{1}, time.Now()); err != nil {
			t.Fatal(err)
		}

		j.Data, _ = hex.DecodeString(data)
		j.Status, j.PoRStatus, j.StatusWord, j.FileSize = status, 0, 0x6282, len(j.Data)
		j.Warning = true

		if err := e.store.FinishJob(context.Background(), j); err != nil {
			t.Fatal(err)
		}

		_, b = e.do(t, http.MethodGet, "/api/v1/jobs/"+itoa(id), "", "")

		var got struct{ Result api.Job }
		if err := json.Unmarshal([]byte(b), &got); err != nil {
			t.Fatal(err)
		}

		return got.Result
	}

	read := finish(`{"iccids":["8988211000000000017"],"operation":"oplmn_read"}`, db.JobSucceeded, "00f110ffff"+"ffffff0000"+"99f9996000")
	if len(read.Entries) != 3 || read.Entries[0].MCC != "001" || read.Entries[0].AccessRaw != "ffff" ||
		read.Entries[1].MCC != "" || read.Entries[1].Access != nil ||
		strings.Join(read.Entries[2].Access, ",") != "eutran_wb_s1" || read.PoRStatus != "por_ok" || read.StatusWord != "6282" ||
		!read.Warning || read.FileSize == nil || *read.FileSize != 15 {
		t.Fatalf("read = %+v", read)
	}

	set := finish(`{"iccids":["8988211000000000017"],"operation":"oplmn_set","slot":7,"mcc":"999","mnc":"99","access":["ngran"]}`,
		db.JobUnconfirmed, "99f9990800")
	if set.Status != "unconfirmed" || len(set.Entries) != 1 || set.Entries[0].Slot != 7 || set.Entries[0].MNC != "99" {
		t.Fatalf("set = %+v", set)
	}
}

// setSMSC sets the SMSC, which jobs need, once.
func setSMSC(t *testing.T, e env) {
	t.Helper()

	if _, body := e.do(t, http.MethodGet, "/api/v1/smsc", "", ""); strings.Contains(body, `"password_set":true`) {
		return
	}

	if code, body := e.do(t, http.MethodPut, "/api/v1/smsc", "application/json", smscBody); code != http.StatusOK {
		t.Fatalf("set SMSC = %d %s", code, body)
	}
}
