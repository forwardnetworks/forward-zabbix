// Derived from the requirements in DESIGN.md, using synthetic devices only.
package plan

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func dev(name, ip string) Device {
	d := Device{Name: name, Site: "dc1", Vendor: "ARISTA", Model: "m", OS: "eos", OSVersion: "1", DeviceType: "SWITCH"}
	if ip != "" {
		d.IPs = []string{ip}
	}
	return d
}

func host(id, name, ip string, groups map[string]bool, tags []Tag, templates map[string]bool) ZbxHost {
	return ZbxHost{HostID: id, Host: name, Name: name, IPs: []string{ip}, Groups: groups, Tags: tags,
		InterfaceID: "i" + id, Templates: templates}
}

var managed = []Tag{{"managed-by", ManagedBy}, {"fwd-network", "1"}}

func driftNames(g Gap) []string {
	var n []string
	for _, d := range g.Drift {
		n = append(n, d.Device)
	}
	sort.Strings(n)
	return n
}

func TestGapFindsUnmonitoredUnmodeledAndDrift(t *testing.T) {
	devices := []Device{dev("a", "10.0.0.1"), dev("b", "10.0.0.2"), dev("c", "10.0.0.3"), dev("d", "10.0.0.4")}
	hosts := []ZbxHost{
		host("1", "a", "10.0.0.1", nil, nil, nil),
		host("2", "B", "10.9.9.9", nil, nil, nil),                                // same name (case-insensitive), wrong IP
		host("3", "c-renamed", "10.0.0.3", nil, nil, nil),                        // same IP, different name
		host("4", "extra", "10.0.0.50", nil, nil, nil),                           // not in Forward
		host("5", "Zabbix server", "127.0.0.1", set("Zabbix servers"), nil, nil), // ignored
	}
	g := CoverageGap(devices, hosts)
	if !reflect.DeepEqual(g.Unmonitored, []string{"d"}) || !reflect.DeepEqual(g.Unmodeled, []string{"extra"}) || g.Matched != 3 {
		t.Fatalf("gap = %+v", g)
	}
	if !reflect.DeepEqual(driftNames(g), []string{"b", "c"}) {
		t.Fatalf("drift = %+v", g.Drift)
	}
}

func TestDeviceWithoutIPIsNotSynced(t *testing.T) {
	p := PlanSync([]Device{dev("noip", "")}, nil, "1")
	if !reflect.DeepEqual(p.SkippedNoIP, []string{"noip"}) || len(p.Create) != 0 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestSyncNeverTouchesUnmanagedHosts(t *testing.T) {
	p := PlanSync([]Device{dev("a", "10.0.0.1"), dev("b", "10.0.0.2")},
		[]ZbxHost{host("1", "a", "10.0.0.1", nil, nil, nil), host("2", "other", "10.0.0.2", nil, nil, nil)}, "1")
	if len(p.Create) != 0 || len(p.Update) != 0 {
		t.Fatalf("touched an unmanaged host: %+v", p)
	}
	var names []string
	for _, c := range p.Collisions {
		names = append(names, c.Device)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("collisions = %v", names)
	}
}

func existingFor(d Device, id, ip string) ZbxHost {
	want := DesiredHost(d, "1")
	return host(id, d.Name, ip, want.Groups, want.Tags, want.Templates)
}

func TestSyncIdempotentAfterCreate(t *testing.T) {
	d := dev("a", "10.0.0.1")
	p := PlanSync([]Device{d}, []ZbxHost{existingFor(d, "1", "10.0.0.1")}, "1")
	if !reflect.DeepEqual(p.Unchanged, []string{"a"}) || len(p.Create) != 0 || len(p.Update) != 0 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestSyncUpdatesChangedIPAndReportsStaleWithoutDeleting(t *testing.T) {
	d := dev("a", "10.0.0.9")
	p := PlanSync([]Device{d}, []ZbxHost{existingFor(d, "1", "10.0.0.1"), host("2", "gone", "10.0.0.7", nil, managed, nil)}, "1")
	if len(p.Update) != 1 || !strings.HasPrefix(p.Update[0].Changes[0], "ip") {
		t.Fatalf("update = %+v", p.Update)
	}
	if !reflect.DeepEqual(p.Stale, []string{"gone"}) {
		t.Fatalf("stale = %v", p.Stale)
	}
}

func TestHostNameSanitisedButVisibleNameKept(t *testing.T) {
	w := DesiredHost(dev("core/01 (lab)", "10.0.0.1"), "1")
	if w.Host != "core_01 _lab_" || w.Name != "core/01 (lab)" {
		t.Fatalf("host %q name %q", w.Host, w.Name)
	}
}

func TestManagedHostWithoutTheDeviceTemplateGetsItAdded(t *testing.T) {
	d := dev("a", "10.0.0.1")
	w := DesiredHost(d, "1")
	p := PlanSync([]Device{d}, []ZbxHost{host("1", "a", "10.0.0.1", w.Groups, w.Tags, nil)}, "1")
	if len(p.Update) != 1 || !reflect.DeepEqual(p.Update[0].Changes, []string{"templates"}) {
		t.Fatalf("update = %+v", p.Update)
	}
}

func TestForeignTemplatesOnAManagedHostAreNotAChange(t *testing.T) {
	d := dev("a", "10.0.0.1")
	w := DesiredHost(d, "1")
	e := host("1", "a", "10.0.0.1", w.Groups, w.Tags, set(DeviceTemplate, "Some Other Template"))
	p := PlanSync([]Device{d}, []ZbxHost{e}, "1")
	if !reflect.DeepEqual(p.Unchanged, []string{"a"}) || len(p.Update) != 0 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestStatusValuesReportFailuresAndSkipDevicesWithNoResult(t *testing.T) {
	ok, bad, unknown := dev("ok", "10.0.0.1"), dev("bad", "10.0.0.2"), dev("unknown", "10.0.0.3")
	ok.Collected, bad.Collected = "completed", "collectionFailed:AUTHENTICATION_FAILED"
	rows := StatusValues([]Device{ok, bad, unknown}, "7", "99", 1700000000)
	has := func(v Value) bool {
		for _, r := range rows {
			if r == v {
				return true
			}
		}
		return false
	}
	for _, v := range []Value{{"forward-network-7", "fwd.devices.total", "3"},
		{"forward-network-7", "fwd.devices.collection_failed", "1"}, {"forward-network-7", "fwd.snapshot.id", "99"},
		{"bad", "fwd.collection.result", "collectionFailed:AUTHENTICATION_FAILED"}} {
		if !has(v) {
			t.Errorf("missing %+v", v)
		}
	}
	for _, r := range rows {
		if r.Host == "unknown" {
			t.Errorf("reported a device with no result: %+v", r)
		}
	}
}

func TestNoteNamesNeighborsOnceWithTheirPortsAndStartsWithTheMarker(t *testing.T) {
	d := dev("core1", "10.0.0.1")
	d.Collected = "completed"
	note := EnrichmentNote(d, []Link{{"Eth1", "rtr-b", "Eth9"}, {"Eth2", "rtr-b", "Eth10"}, {"Eth3", "rtr-a", "Eth1"}}, "42")
	for _, want := range []string{"[Forward]", "snapshot 42", "2 device(s) over 3 link(s)", "Eth1<->Eth9, Eth2<->Eth10"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Count(note, "rtr-b:") != 1 || strings.Index(note, "rtr-a:") > strings.Index(note, "rtr-b:") {
		t.Errorf("peers not listed once, in order:\n%s", note)
	}
}

func TestNoteSaysSoWhenForwardCouldNotCollectTheDevice(t *testing.T) {
	d := dev("fw1", "10.0.0.2")
	d.Collected = "collectionFailed:AUTHENTICATION_FAILED"
	note := EnrichmentNote(d, nil, "42")
	for _, want := range []string{"could not collect", "AUTHENTICATION_FAILED", "no links"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q", want)
		}
	}
}

func TestNoteFitsTheZabbixMessageLimitForADeviceWithManyNeighbors(t *testing.T) {
	var links []Link
	for i := 0; i < 400; i++ {
		links = append(links, Link{fmt.Sprintf("Eth%d", i), fmt.Sprintf("peer-number-%04d", i), "Eth1"})
	}
	if n := len([]rune(EnrichmentNote(dev("hub", "10.0.0.3"), links, "1"))); n > 2048 {
		t.Fatalf("note is %d characters", n)
	}
}

func TestTheNetworkCarrierHostIsNotStaleAndNotUnmodeled(t *testing.T) {
	carrier := host("9", "forward-network-1", "127.0.0.1", nil, managed, nil)
	if s := PlanSync(nil, []ZbxHost{carrier}, "1").Stale; len(s) != 0 {
		t.Fatalf("stale = %v", s)
	}
	if u := CoverageGap(nil, []ZbxHost{carrier}).Unmodeled; len(u) != 0 {
		t.Fatalf("unmodeled = %v", u)
	}
}

func TestAUserHostThatMerelyLooksLikeTheCarrierIsStillReported(t *testing.T) {
	lookalike := host("9", "forward-network-1", "10.9.9.9", nil, nil, nil) // not managed by this tool
	if u := CoverageGap(nil, []ZbxHost{lookalike}).Unmodeled; !reflect.DeepEqual(u, []string{"forward-network-1"}) {
		t.Fatalf("unmodeled = %v", u)
	}
}

func TestStatusRowsMatchByNameThenIPAndCountProblems(t *testing.T) {
	mk := func(n, ip string) Device {
		return Device{Name: n, Site: "dc1", Vendor: "ARISTA", Model: "m", OS: "EOS", OSVersion: "1", DeviceType: "SWITCH", IPs: []string{ip}, Collected: "completed"}
	}
	devs := []Device{mk("core-1", "10.0.0.1"), mk("edge-1", "10.0.0.2"), mk("lonely", "10.0.0.9")}
	hosts := []ZbxHost{host("1", "core-1", "10.0.0.1", nil, nil, nil), host("2", "old-name", "10.0.0.2", nil, nil, nil)}
	rows := StatusRows(devs, hosts, map[string]ProblemCount{"core-1": {2, 4}, "old-name": {1, 2}})
	want := []map[string]string{
		{"device": "core-1", "monitored": "yes", "zabbix_host": "core-1", "open_problems": "2", "worst_severity": "high"},
		{"device": "edge-1", "monitored": "yes", "zabbix_host": "old-name", "open_problems": "1", "worst_severity": "warning"},
		{"device": "lonely", "monitored": "no", "zabbix_host": "", "open_problems": "0", "worst_severity": ""},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestExtraTemplatesAreLinkedAlongsideTheDeviceTemplate(t *testing.T) {
	d := Device{Name: "r1", IPs: []string{"10.0.0.1"}}
	got := PlanSync([]Device{d}, nil, "1", PerfTemplate)
	if len(got.Create) != 1 || !got.Create[0].Templates[DeviceTemplate] || !got.Create[0].Templates[PerfTemplate] {
		t.Fatalf("%+v", got.Create)
	}
	if plain := PlanSync([]Device{d}, nil, "1"); plain.Create[0].Templates[PerfTemplate] {
		t.Fatal("the performance template must be opt-in")
	}
}
