package plan

import (
	"reflect"
	"strings"
	"testing"
)

// A small fixture: hosts h-<name> with trigger t-<name>, all owned by network "n1".
func topoFixture(names ...string) ([]Device, []ZbxHost, []ReachTrigger) {
	var ds []Device
	var hs []ZbxHost
	var ts []ReachTrigger
	for _, n := range names {
		ds = append(ds, Device{Name: n})
		hs = append(hs, ZbxHost{HostID: "h-" + n, Host: n, Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}}})
		ts = append(ts, ReachTrigger{HostID: "h-" + n, TriggerID: "t-" + n})
	}
	return ds, hs, ts
}

func chain(pairs ...string) map[string][]Link {
	m := map[string][]Link{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = append(m[pairs[i]], Link{Peer: pairs[i+1]})
	}
	return m
}

func changesByHost(p TopologyPlan) map[string]string {
	m := map[string]string{}
	for _, c := range p.Change {
		m[c.Host] = c.Parent
	}
	return m
}

func TestPlanTopologyChain(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b", "c")
	p, err := PlanTopology(ds, chain("a", "b", "b", "c"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := changesByHost(p), map[string]string{"b": "a", "c": "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(p.Roots, []string{"a"}) {
		t.Errorf("roots = %v, want [a]", p.Roots)
	}
}

func TestPlanTopologyIsIdempotent(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b", "c")
	ts[1].Deps = []string{"t-a"}
	ts[2].Deps = []string{"t-b"}
	p, err := PlanTopology(ds, chain("a", "b", "b", "c"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Change) != 0 {
		t.Fatalf("a second run must change nothing, got %+v", p.Change)
	}
	if !reflect.DeepEqual(p.Unchanged, []string{"a", "b", "c"}) {
		t.Errorf("unchanged = %v", p.Unchanged)
	}
}

// Dependencies this tool does not own (a trigger of the same host, something a person added) are kept.
func TestPlanTopologyKeepsForeignDependencies(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b")
	ts[1].Deps = []string{"same-host-icmp", "human-made"}
	p, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Change) != 1 {
		t.Fatalf("want one change, got %+v", p.Change)
	}
	want := []string{"human-made", "same-host-icmp", "t-a"}
	if !reflect.DeepEqual(p.Change[0].Want, want) {
		t.Errorf("want list = %v, want %v", p.Change[0].Want, want)
	}
}

// Moving the vantage turns the tree around; the old dependency must be dropped, not left beside the new one.
func TestPlanTopologyVantageMoved(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b")
	ts[0].Deps = []string{"t-b"} // set earlier when b was the vantage
	p, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DepChange{}
	for _, c := range p.Change {
		got[c.Host] = c
	}
	if c := got["a"]; c.Parent != "" || !reflect.DeepEqual(c.Was, []string{"b"}) || len(c.Want) != 0 {
		t.Errorf("a should lose its dependency on b, got %+v", c)
	}
	if c := got["b"]; c.Parent != "a" {
		t.Errorf("b should depend on a, got %+v", c)
	}
}

func TestPlanTopologyRemovesDependenciesOfGoneAndUnreachableDevices(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b", "x")
	// a host with no Forward device any more, still carrying one of our dependencies
	hs = append(hs, ZbxHost{HostID: "h-old", Host: "old", Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}}})
	ts = append(ts, ReachTrigger{HostID: "h-old", TriggerID: "t-old", Deps: []string{"t-a"}})
	ts[2].Deps = []string{"t-a"} // x is no longer reachable from the vantage
	p, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := changesByHost(p), map[string]string{"b": "a", "old": "", "x": ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(p.Unreachable, []string{"x"}) {
		t.Errorf("unreachable = %v", p.Unreachable)
	}
}

func TestPlanTopologyOwnership(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b", "c", "d")
	hs[1].Tags = nil                                                        // b: hand-made host with the device's name
	hs[2].Tags = []Tag{{"managed-by", ManagedBy}, {"fwd-network", "other"}} // c: another network's host
	ts[3].HostID = "h-no-such-host"                                         // d: owned, but its trigger is not on any host: template missing
	p, err := PlanTopology(ds, chain("a", "b", "b", "c", "c", "d"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Change) != 0 {
		t.Errorf("must not touch hosts it does not own or that lack the trigger, got %+v", p.Change)
	}
	if !reflect.DeepEqual(p.NoTrigger, []string{"d"}) {
		t.Errorf("no_trigger = %v, want [d]", p.NoTrigger)
	}
	if len(p.Left) != 2 || !strings.HasPrefix(p.Left[0], "b:") || !strings.HasPrefix(p.Left[1], "c:") {
		t.Errorf("left = %v, want b and c", p.Left)
	}
}

// A dependency onto a host this sync does not own is not this tool's to remove, even if it is a reachability trigger.
func TestPlanTopologyKeepsDependenciesOntoHostsItDoesNotOwn(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b", "c", "x")
	hs[2].Tags = []Tag{{"managed-by", ManagedBy}, {"fwd-network", "other"}} // c belongs to another network
	hs[3].Tags = nil                                                        // x is hand-made
	ts[1].Deps = []string{"t-c", "t-x"}
	p, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Change) != 1 || !reflect.DeepEqual(p.Change[0].Want, []string{"t-a", "t-c", "t-x"}) {
		t.Fatalf("both foreign dependencies must be kept beside the new one, got %+v", p.Change)
	}
}

func TestPlanTopologyNameClashIsLeftAlone(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b")
	ds = append(ds, Device{Name: "B"}) // folds to the same Zabbix host name as b
	ts[1].Deps = []string{"t-a"}       // b's host already carries one of ours; two devices claim it, so leave it be
	ts[0].Deps = []string{"t-b"}
	p, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Left) != 2 {
		t.Errorf("both clashing devices must be left alone, got %v", p.Left)
	}
	for _, c := range p.Change {
		if c.Host == "b" {
			t.Errorf("a host two devices claim must not be changed, got %+v", c)
		}
	}
}

func TestPlanTopologyGoneDeviceHasNoDeviceName(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b")
	ts[1].Deps = []string{"t-a"} // b is already right
	hs = append(hs, ZbxHost{HostID: "h-old", Host: "old", Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}}})
	ts = append(ts, ReachTrigger{HostID: "h-old", TriggerID: "t-old", Deps: []string{"t-a"}})
	p, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Change) != 1 || p.Change[0].Device != "" || p.Change[0].Host != "old" {
		t.Errorf("want one change on host old with no device name, got %+v", p.Change)
	}
}

func TestIDLessIsNumeric(t *testing.T) {
	if !idLess("999", "1000") || idLess("1000", "999") || idLess("5", "5") {
		t.Error("ids must order numerically, not as text")
	}
}

func TestPlanTopologyVantageMustBeADevice(t *testing.T) {
	ds, hs, ts := topoFixture("a", "b")
	if _, err := PlanTopology(ds, chain("a", "b"), hs, ts, "n1", []string{"nope"}); err == nil {
		t.Fatal("want an error for a vantage that is not a Forward device")
	}
}
