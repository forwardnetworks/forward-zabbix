package fwdzbx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/perf"
)

func TestNewerThanKeepsOnlyTicksAfterTheWatermark(t *testing.T) {
	ticks := []perf.DevicePerf{{Time: time.Unix(100, 0)}, {Time: time.Unix(200, 0)}, {Time: time.Unix(300, 0)}}
	got := newerThan(ticks, 200, 1000)
	if len(got) != 1 || got[0].Time.Unix() != 300 {
		t.Fatalf("%+v", got)
	}
	if len(newerThan(ticks, 0, 1000)) != 3 {
		t.Fatal("no watermark must keep everything")
	}
}

func TestStateRoundTripsIsPrivateAndAMissingFileIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "state.json")
	st, err := LoadPerfState(p)
	if err != nil || len(st.Newest) != 0 {
		t.Fatalf("missing file: %+v %v", st, err)
	}
	st.Newest["r1"] = 1234
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	back, err := LoadPerfState(p)
	if err != nil || back.Newest["r1"] != 1234 {
		t.Fatalf("%+v %v", back, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestADamagedStateFileIsAnErrorNotAReset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(p, []byte("{not json"), 0o600)
	if _, err := LoadPerfState(p); err == nil {
		t.Fatal("a damaged state file must not be silently treated as empty")
	}
}

func TestDefaultStatePathPrefersExplicitThenSystemdThenHome(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := DefaultPerfStatePath(env(map[string]string{"FWDZBX_STATE_FILE": "/x/s.json", "STATE_DIRECTORY": "/var/lib/fwdzbx"})); got != "/x/s.json" {
		t.Fatal(got)
	}
	if got := DefaultPerfStatePath(env(map[string]string{"STATE_DIRECTORY": "/var/lib/fwdzbx"})); got != "/var/lib/fwdzbx/perf-state.json" {
		t.Fatal(got)
	}
}

func TestTicksNewerThanTheSettlingLimitAreHeldBack(t *testing.T) {
	ticks := []perf.DevicePerf{{Time: time.Unix(100, 0)}, {Time: time.Unix(200, 0)}, {Time: time.Unix(300, 0)}}
	if got := newerThan(ticks, 0, 250); len(got) != 2 || got[1].Time.Unix() != 200 {
		t.Fatalf("%+v", got)
	}
}

func TestOnlyCumulativeSeriesCanBePushedOnASchedule(t *testing.T) {
	h := perf.Device{Ifaces: []perf.Series{{Name: "e1", Cumulative: true}, {Name: "lo", Cumulative: false}}}
	got, why := pushable(h)
	if why != "" || len(got.Ifaces) != 1 || got.Ifaces[0].Name != "e1" {
		t.Fatalf("%+v %q", got, why)
	}
	if _, why := pushable(perf.Device{Ifaces: []perf.Series{{Name: "e1"}}}); why == "" {
		t.Fatal("a stock-template-only device must be skipped with a reason")
	}
}

func TestAClaimSurvivesTheStateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	st, _ := LoadPerfState(p)
	st.Claimed["r1"] = true
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	back, _ := LoadPerfState(p)
	if !back.Claimed["r1"] {
		t.Fatal("claim lost")
	}
}
