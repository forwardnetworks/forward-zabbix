package plan

import (
	"reflect"
	"strings"
	"testing"
)

func svcHosts(names ...string) ([]Device, []ZbxHost) {
	var ds []Device
	var hs []ZbxHost
	for _, n := range names {
		ds = append(ds, Device{Name: n})
		hs = append(hs, ZbxHost{HostID: "h-" + n, Host: n, Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}, {"fwd-device", n}}})
	}
	return ds, hs
}

// materialize turns wanted services into the services Zabbix would then hold, with made-up ids.
func materialize(wants []SvcWant) []Svc {
	id := map[string]string{}
	for i, w := range wants {
		id[w.Key] = "s" + string(rune('A'+i))
	}
	var out []Svc
	for _, w := range wants {
		s := Svc{ID: id[w.Key], Name: w.Name, Algorithm: SvcAlgorithm, Tags: w.Tags, ProblemTags: w.ProblemTags}
		if w.ParentKey != "" {
			s.ParentIDs = []string{id[w.ParentKey]}
		}
		out = append(out, s)
	}
	return out
}

func wantByKey(p ServicesPlan) map[string]SvcWant {
	m := map[string]SvcWant{}
	for _, w := range p.Create {
		m[w.Key] = w
	}
	return m
}

func TestPlanServicesBuildsARootAndAContainerPlusLeafPerDevice(t *testing.T) {
	ds, hs := svcHosts("a", "b", "c")
	p, err := PlanServices(ds, chain("a", "b", "b", "c"), hs, nil, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Create) != 7 {
		t.Fatalf("want a root plus a container and a leaf for 3 devices = 7, got %d: %+v", len(p.Create), p.Create)
	}
	w := wantByKey(p)
	if w["network|"].ParentKey != "" || w["device|a"].ParentKey != "network|" || w["device|b"].ParentKey != "device|a" || w["device|c"].ParentKey != "device|b" {
		t.Errorf("containers must nest as the topology does: %+v", w)
	}
	for _, d := range []string{"a", "b", "c"} {
		leaf := w["self|"+d]
		if leaf.ParentKey != "device|"+d || len(leaf.ProblemTags) != 1 || leaf.ProblemTags[0] != (Tag{"fwd-device", d}) {
			t.Errorf("leaf for %s must sit under its container and match its problems: %+v", d, leaf)
		}
		if len(w["device|"+d].ProblemTags) != 0 {
			t.Errorf("a container has children, so Zabbix forbids problem tags on it: %+v", w["device|"+d])
		}
	}
	// a parent must be created before anything under it
	seen := map[string]bool{}
	for _, c := range p.Create {
		if c.ParentKey != "" && !seen[c.ParentKey] {
			t.Errorf("%s is created before its parent %s", c.Key, c.ParentKey)
		}
		seen[c.Key] = true
	}
}

func TestPlanServicesIsIdempotent(t *testing.T) {
	ds, hs := svcHosts("a", "b")
	first, _ := PlanServices(ds, chain("a", "b"), hs, nil, "n1", []string{"a"})
	p, err := PlanServices(ds, chain("a", "b"), hs, materialize(first.Create), "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Create) != 0 || len(p.Update) != 0 || len(p.Unchanged) != 5 {
		t.Fatalf("a second run must change nothing: create %d update %d unchanged %d", len(p.Create), len(p.Update), len(p.Unchanged))
	}
}

func TestPlanServicesSkipsAHostWithoutTheDeviceTag(t *testing.T) {
	ds, hs := svcHosts("a", "b")
	hs[1].Tags = []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}} // synced before the tag existed
	p, err := PlanServices(ds, chain("a", "b"), hs, nil, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wantByKey(p)["device|b"]; ok {
		t.Error("a service for a device whose problems it cannot match would sit green while the device is down")
	}
	if len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0], "sync-inventory --apply") {
		t.Errorf("skipped = %v, want b with the instruction to run sync-inventory", p.Skipped)
	}
}

func TestPlanServicesMovesADeviceAndKeepsAPersonsParent(t *testing.T) {
	ds, hs := svcHosts("a", "b", "c")
	first, _ := PlanServices(ds, chain("a", "b", "a", "c"), hs, nil, "n1", []string{"a"}) // b and c both under a
	have := materialize(first.Create)
	var cID string
	for i := range have {
		switch have[i].Name {
		case "c":
			have[i].ParentIDs = append(have[i].ParentIDs, "human-made-service") // a person also filed c under their own service
			cID = have[i].ID
		}
	}
	p, err := PlanServices(ds, chain("a", "b", "b", "c"), hs, have, "n1", []string{"a"}) // c now sits behind b
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Update) != 1 || p.Update[0].ID != cID || !strings.Contains(strings.Join(p.Update[0].Changes, ";"), "parent") {
		t.Fatalf("want one update moving c, got %+v", p.Update)
	}
	if !reflect.DeepEqual(p.Update[0].KeepParents, []string{"human-made-service"}) {
		t.Errorf("a parent this tool does not own must be kept, got %v", p.Update[0].KeepParents)
	}
	if p.Update[0].Want.ParentKey != "device|b" {
		t.Errorf("c must move under b, wants parent %q", p.Update[0].Want.ParentKey)
	}
}

func TestPlanServicesLeavesStaleAndForeignServicesAlone(t *testing.T) {
	ds, hs := svcHosts("a")
	first, _ := PlanServices(ds, chain("a", "z"), hs, nil, "n1", []string{"a"})
	have := materialize(first.Create)
	gone := Svc{ID: "old", Name: "gone", Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}, {"fwd-service", SvcDevice}, {"fwd-device", "gone"}}}
	other := Svc{ID: "o", Name: "a", Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "other"}, {"fwd-service", SvcDevice}, {"fwd-device", "a"}}}
	human := Svc{ID: "h", Name: "a"} // a person's service that happens to share the name
	p, err := PlanServices(ds, chain("a", "z"), hs, append(have, gone, other, human), "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Stale, []string{"gone"}) {
		t.Errorf("stale = %v, want [gone]", p.Stale)
	}
	if len(p.Create) != 0 || len(p.Update) != 0 {
		t.Errorf("another network's or a person's service must not be matched or changed: %+v %+v", p.Create, p.Update)
	}
}

func TestPlanServicesPutsAnUnreachableDeviceUnderTheNetwork(t *testing.T) {
	ds, hs := svcHosts("a", "b", "x")
	p, err := PlanServices(ds, chain("a", "b"), hs, nil, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wantByKey(p)["device|x"].ParentKey; got != "network|" {
		t.Errorf("x has no path to the vantage, so it hangs off the network; got parent %q", got)
	}
	if !reflect.DeepEqual(p.Unreachable, []string{"x"}) {
		t.Errorf("unreachable = %v", p.Unreachable)
	}
}

func TestPlanServicesVantageMustBeADevice(t *testing.T) {
	ds, hs := svcHosts("a")
	if _, err := PlanServices(ds, chain("a", "b"), hs, nil, "n1", []string{"nope"}); err == nil {
		t.Fatal("want an error for a vantage that is not a Forward device")
	}
}

// find returns the service named name from a materialized set.
func find(have []Svc, name string) *Svc {
	for i := range have {
		if have[i].Name == name {
			return &have[i]
		}
	}
	return nil
}

func TestPlanServicesRepairsWhatSomeoneChangedByHand(t *testing.T) {
	ds, hs := svcHosts("a", "b")
	first, _ := PlanServices(ds, chain("a", "b"), hs, nil, "n1", []string{"a"})
	have := materialize(first.Create)
	find(have, "a").Name = "renamed by hand"
	find(have, "b").Algorithm = "1"
	find(have, "b (device itself)").ProblemTags = []Tag{{"fwd-device", "other"}}
	p, err := PlanServices(ds, chain("a", "b"), hs, have, "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range p.Update {
		got[u.Want.Key] = strings.Join(u.Changes, ";")
	}
	for key, frag := range map[string]string{"device|a": "name", "device|b": "status rule", "self|b": "problem tags"} {
		if !strings.Contains(got[key], frag) {
			t.Errorf("%s: changes %q, want one mentioning %q (all: %v)", key, got[key], frag, got)
		}
	}
	if len(p.Update) != 3 {
		t.Errorf("only the three changed services should update, got %d", len(p.Update))
	}
}

func TestPlanServicesLeavesDuplicatesAloneAndReportsThem(t *testing.T) {
	ds, hs := svcHosts("a")
	first, _ := PlanServices(ds, chain("a", "z"), hs, nil, "n1", []string{"a"})
	have := materialize(first.Create)
	clone := *find(have, "a")
	clone.ID = "clone"
	p, err := PlanServices(ds, chain("a", "z"), hs, append(have, clone), "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Duplicates, []string{"device|a"}) {
		t.Errorf("duplicates = %v", p.Duplicates)
	}
	for _, c := range p.Create {
		if c.Key == "device|a" {
			t.Error("must not create a third copy")
		}
	}
	for _, u := range p.Update {
		if u.Want.Key == "device|a" {
			t.Error("must not pick one of two duplicates to change")
		}
	}
	if len(p.Stale) != 0 {
		t.Errorf("a duplicated service is not stale: %v", p.Stale)
	}
}

func TestPlanServicesSkipsDevicesThatShareAHostName(t *testing.T) {
	ds := []Device{{Name: "Core-1"}, {Name: "core-1"}, {Name: "ok"}}
	hs := []ZbxHost{
		{HostID: "1", Host: "core-1", Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}, {"fwd-device", "core-1"}}},
		{HostID: "2", Host: "ok", Tags: []Tag{{"managed-by", ManagedBy}, {"fwd-network", "n1"}, {"fwd-device", "ok"}}},
	}
	p, err := PlanServices(ds, chain("ok", "core-1", "ok", "Core-1"), hs, nil, "n1", []string{"ok"})
	if err != nil {
		t.Fatal(err)
	}
	w := wantByKey(p)
	if _, ok := w["device|core-1"]; ok {
		t.Error("two devices claim one host, so neither gets a service")
	}
	if _, ok := w["device|ok"]; !ok || len(p.Skipped) != 2 {
		t.Errorf("want ok built and both clashing devices skipped, got skipped %v", p.Skipped)
	}
}

func TestPlanServicesWithTwoVantagesPutsTheSharedDeviceBehindNeitherOfThem(t *testing.T) {
	ds, hs := svcHosts("v1", "v2", "x", "leaf")
	p, err := PlanServices(ds, chain("v1", "x", "v2", "x", "x", "leaf"), hs, nil, "n1", []string{"v1", "v2"})
	if err != nil {
		t.Fatal(err)
	}
	w := wantByKey(p)
	if w["device|x"].ParentKey != "network|" || w["device|leaf"].ParentKey != "device|x" {
		t.Errorf("x is behind both vantages so it hangs off the network; leaf is behind x: %+v", w)
	}
}

func TestPlanServicesReordersChildrenAfterTheirNewParents(t *testing.T) {
	ds, hs := svcHosts("a", "b", "c")
	first, _ := PlanServices(ds, chain("a", "b", "b", "c"), hs, nil, "n1", []string{"a"})
	p, err := PlanServices(ds, chain("a", "c", "c", "b"), hs, materialize(first.Create), "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, u := range p.Update {
		pos[u.Want.Key] = i
	}
	if len(p.Update) != 2 || pos["device|c"] > pos["device|b"] {
		t.Errorf("c must be re-parented before b is put under it: %+v", p.Update)
	}
}

// With two services claiming a device, a child under it cannot be given "the" parent; it must not be planned as a
// move that the apply step could only fail on.
func TestPlanServicesDoesNotMoveChildrenOfADuplicate(t *testing.T) {
	ds, hs := svcHosts("a", "b")
	first, _ := PlanServices(ds, chain("a", "b"), hs, nil, "n1", []string{"a"})
	have := materialize(first.Create)
	clone := *find(have, "a")
	clone.ID = "clone"
	p, err := PlanServices(ds, chain("a", "b"), hs, append(have, clone), "n1", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Update) != 0 || len(p.Create) != 0 {
		t.Errorf("nothing else should change: create %+v update %+v", p.Create, p.Update)
	}
	// ...but if that child needs another change, the update must carry its current parent through untouched
	have2 := append(append([]Svc(nil), have...), clone)
	find(have2, "b").Name = "renamed by hand"
	p, _ = PlanServices(ds, chain("a", "b"), hs, have2, "n1", []string{"a"})
	if len(p.Update) != 1 || p.Update[0].Want.ParentKey != "" || !reflect.DeepEqual(p.Update[0].KeepParents, find(have, "b").ParentIDs) {
		t.Errorf("an update under a duplicate must resend the current parents, got %+v", p.Update)
	}
}
