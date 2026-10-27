package forward

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestAroundPicksTheSnapshotsOnEitherSideOfTheWindow(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC) }
	snaps := []Snap{{"1", at(1)}, {"2", at(3)}, {"3", at(5)}, {"4", at(7)}}
	b, a, err := Around(snaps, at(4), at(6)) // window 04:00-06:00
	if err != nil || b.ID != "2" || a.ID != "4" {
		t.Fatalf("got before %v after %v err %v, want 2 and 4", b, a, err)
	}
	b, a, err = Around(snaps, at(3), at(5)) // snapshots exactly on the edges count
	if err != nil || b.ID != "2" || a.ID != "3" {
		t.Fatalf("edges: got before %v after %v err %v, want 2 and 3", b, a, err)
	}
	if _, _, err = Around(snaps, at(4), at(9)); err == nil || !strings.Contains(err.Error(), "yet") {
		t.Errorf("no snapshot after the window must say so, got %v", err)
	}
	if _, _, err = Around(snaps, at(0), at(2)); err == nil {
		t.Error("no snapshot before the window must be an error")
	}
}

func TestUsableDropsDraftsPredictionsAndUnprocessedAndSortsByTime(t *testing.T) {
	mk := func(id, state, created string, draft, pred bool) sdk.Snapshot {
		return sdk.Snapshot{ID: sdk.Identifier(id), State: state, CreatedAt: created, IsDraft: draft, IsPredicted: pred}
	}
	got := usable([]sdk.Snapshot{
		mk("late", "PROCESSED", "2026-01-02T00:00:00Z", false, false),
		mk("draft", "PROCESSED", "2026-01-01T12:00:00Z", true, false),
		mk("pred", "PROCESSED", "2026-01-01T13:00:00Z", false, true),
		mk("busy", "PROCESSING", "2026-01-01T14:00:00Z", false, false),
		mk("bad-time", "PROCESSED", "yesterday", false, false),
		mk("early", "PROCESSED", "2026-01-01T00:00:00Z", false, false),
	})
	if len(got) != 2 || got[0].ID != "early" || got[1].ID != "late" {
		t.Fatalf("got %+v, want early then late only", got)
	}
}

func TestConfigDevicesListsEachChangedDeviceOnceSortedAndSkipsEmptyOnes(t *testing.T) {
	d := func(name string, files ...string) sdk.DeviceFilesDiff {
		out := sdk.DeviceFilesDiff{InventoryDeviceInfo: sdk.InventoryDeviceInfo{Name: name}}
		for _, f := range files {
			out.Files = append(out.Files, sdk.DiffFile{Name: f})
		}
		return out
	}
	got := configDevices([]sdk.DeviceFilesDiff{d("z-2", "f"), d("a-1", "f", "g"), d("no-files"), d("", "f"), d("z-2", "f")})
	if strings.Join(got, ",") != "a-1,z-2" {
		t.Errorf("got %v, want a-1,z-2", got)
	}
	if configDevices(nil) == nil {
		t.Error("an empty result must be an empty list, not nil")
	}
}

func TestUsableOrdersSnapshotsWithTheSameTimeByID(t *testing.T) {
	same := "2026-01-01T00:00:00Z"
	got := usable([]sdk.Snapshot{{ID: "9", State: "PROCESSED", CreatedAt: same}, {ID: "10", State: "PROCESSED", CreatedAt: same}})
	if len(got) != 2 || got[0].ID != "10" || got[1].ID != "9" {
		t.Errorf("got %+v, want a fixed order (10 before 9 as text)", got)
	}
}
