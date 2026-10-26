package forward

import (
	"encoding/json"
	"testing"

	sdk "github.com/forwardnetworks/forward-go-sdk"
)

func rec(t *testing.T, js string) sdk.NQERecord {
	t.Helper()
	var r sdk.NQERecord
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// snapshotInfo.result is a union: a plain string, or {case, value} for a failure.
func TestCollectedNormalisesTheUnion(t *testing.T) {
	for js, want := range map[string]string{
		`{"collected":"completed"}`: "completed",
		`{"collected":{"case":"collectionFailed","value":"AUTHENTICATION_FAILED"}}`: "collectionFailed:AUTHENTICATION_FAILED",
		`{"collected":{"case":"unreachable","value":null}}`:                         "unreachable",
		`{"collected":null}`: "",
		`{}`:                 "",
	} {
		if got := collected(rec(t, js)); got != want {
			t.Errorf("%s -> %q, want %q", js, got, want)
		}
	}
}

func TestRecordHelpersTolerateMissingAndNullFields(t *testing.T) {
	r := rec(t, `{"name":"a","site":null,"ips":["10.0.0.1"],"tags":null}`)
	if str(r, "name") != "a" || str(r, "site") != "" || str(r, "absent") != "" {
		t.Error("str")
	}
	if got := strs(r, "ips"); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("ips %v", got)
	}
	if got := strs(r, "tags"); len(got) != 0 {
		t.Errorf("tags %v", got)
	}
}

// The newest non-draft, non-predicted PROCESSED snapshot by creation time wins, wherever it is in the listing.
func TestPickNewestIgnoresDraftsPredictionsAndUnprocessedAndOrdersByCreation(t *testing.T) {
	list := []sdk.Snapshot{
		{ID: "1", State: "PROCESSED", CreatedAt: "2026-10-26T10:00:00Z"},
		{ID: "3", State: "PROCESSED", CreatedAt: "2026-10-26T12:00:00Z"},
		{ID: "2", State: "PROCESSED", CreatedAt: "2026-10-26T11:00:00Z"},
		{ID: "9", State: "PROCESSED", CreatedAt: "2026-10-26T13:00:00Z", IsDraft: true},
		{ID: "8", State: "PROCESSED", CreatedAt: "2026-10-26T14:00:00Z", IsPredicted: true},
		{ID: "7", State: "PROCESSING", CreatedAt: "2026-10-26T15:00:00Z"},
		{ID: "6", State: "PROCESSED", CreatedAt: "not a time"},
	}
	got, err := pickNewest("n", list)
	if err != nil || got.ID != "3" || got.Epoch != 1793016000 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if _, err := pickNewest("n", list[3:]); err == nil {
		t.Fatal("expected an error when nothing is usable")
	}
}
