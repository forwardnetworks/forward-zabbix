package fwdzbx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/zabbix"
)

func TestEvidenceInputsAcceptOneWayOfNamingTheChange(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)
	m := &zabbix.Maintenance{Name: "win", Since: t1, Till: t2}
	cases := []struct {
		name    string
		r       EvidenceRequest
		m       *zabbix.Maintenance
		mode    evidenceMode
		since   time.Time
		until   time.Time
		wantErr string
	}{
		{"two snapshot ids", EvidenceRequest{Before: "1", After: "2"}, nil, byIDs, time.Time{}, time.Time{}, ""},
		{"a maintenance gives its own times", EvidenceRequest{Maintenance: "win"}, m, byWindow, t1, t2, ""},
		{"times given with a maintenance replace its own", EvidenceRequest{Maintenance: "win", Since: t1.Add(time.Hour)}, m, byWindow, t1.Add(time.Hour), t2, ""},
		{"an explicit window", EvidenceRequest{Since: t1, Until: t2}, nil, byWindow, t1, t2, ""},
		{"only one snapshot id", EvidenceRequest{Before: "1"}, nil, 0, time.Time{}, time.Time{}, "both --before and --after"},
		{"snapshot ids and a maintenance", EvidenceRequest{Before: "1", After: "2", Maintenance: "win"}, m, 0, time.Time{}, time.Time{}, "cannot be combined"},
		{"snapshot ids and a window", EvidenceRequest{Before: "1", After: "2", Since: t1, Until: t2}, nil, 0, time.Time{}, time.Time{}, "cannot be combined"},
		{"nothing named", EvidenceRequest{}, nil, 0, time.Time{}, time.Time{}, "give --before and --after"},
		{"half a window", EvidenceRequest{Since: t1}, nil, 0, time.Time{}, time.Time{}, "both --since and --until"},
		{"an empty window", EvidenceRequest{Since: t2, Until: t1}, nil, 0, time.Time{}, time.Time{}, "is empty"},
		{"a maintenance with no active period", EvidenceRequest{Maintenance: "win"}, &zabbix.Maintenance{Name: "win", Since: time.Unix(0, 0).UTC(), Till: time.Unix(0, 0).UTC()}, 0, time.Time{}, time.Time{}, "is empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, since, until, err := evidenceInputs(c.r, c.m)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil || mode != c.mode || !since.Equal(c.since) || !until.Equal(c.until) {
				t.Errorf("got mode %v %v-%v err %v, want mode %v %v-%v", mode, since, until, err, c.mode, c.since, c.until)
			}
		})
	}
}

// A maintenance names Zabbix hosts; the expectation is the Forward devices those hosts stand for.
func TestDevicesOfHostsMapsZabbixHostsBackToForwardDevices(t *testing.T) {
	devs := []Device{{Name: "Core-1"}, {Name: "edge/2"}, {Name: "other"}}
	got, rest := devicesOfHosts(devs, []string{"core-1", "edge_2", "not-a-device"})
	if want := []string{"Core-1", "edge/2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(rest, []string{"not-a-device"}) {
		t.Errorf("hosts that stand for no device must be reported, got %v", rest)
	}
}

func TestEvidenceJSONHasEmptyListsNotNull(t *testing.T) {
	b, err := Evidence{}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("an empty report must have [] and {} instead of null: %s", b)
	}
}

// A bad request is a usage error (exit 64), reported before anything is looked up.
func TestChangeEvidenceRejectsABadRequestBeforeAnyLookup(t *testing.T) {
	_, err := ChangeEvidence(context.Background(), "1", EvidenceRequest{Before: "1", After: "2", Maintenance: "typo"}, Options{})
	if !errors.Is(err, config.ErrConfig) || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v, want a config (usage) error about combining", err)
	}
}
