package plan

import (
	"fmt"
	"sort"

	"github.com/forwardnetworks/forward-zabbix/internal/topology"
)

// ReachTag and ReachValue mark the one trigger per host that topology dependencies are set on: the "Forward: ... is
// not answering SNMP" trigger in the Forward Device template.
const (
	ReachTag   = "fwd-role"
	ReachValue = "reachability"
)

// ReachTrigger is the reachability trigger of one Zabbix host and the triggers it depends on today.
type ReachTrigger struct {
	HostID    string
	TriggerID string
	Deps      []string // trigger ids
}

// DepChange is one host whose reachability trigger needs a different dependency list.
type DepChange struct {
	Device    string   `json:"device"` // empty when the host has no Forward device any more
	Host      string   `json:"host"`
	Was       []string `json:"was"`    // hosts it depended on through this tool
	Parent    string   `json:"parent"` // host it should depend on; empty means none
	TriggerID string   `json:"-"`
	Keep      []string `json:"-"` // dependencies this tool does not own, kept as they are
	Want      []string `json:"-"` // the full list after the change
}

// TopologyPlan is what a topology sync would do. It only changes dependencies on this tool's own reachability
// triggers; it never touches a host it does not manage and never deletes anything.
type TopologyPlan struct {
	Vantages          []string    `json:"vantages"`
	Change            []DepChange `json:"change"`
	Unchanged         []string    `json:"unchanged"`
	Roots             []string    `json:"roots"`       // managed, reachable, nothing managed above them
	Unreachable       []string    `json:"unreachable"` // managed, but no link path to a vantage
	NoTrigger         []string    `json:"no_trigger"`  // managed host without the reachability trigger (template not imported or linked)
	Left              []string    `json:"left"`        // devices left alone: no Zabbix host, a host this sync does not own, or a name clash
	UnmanagedVantages []string    `json:"unmanaged_vantages"`
	// Skipped maps a device to the unmanaged devices between it and the parent it depends on.
	Skipped map[string][]string `json:"skipped"`
}

// PlanTopology works out which managed host should depend on which. A dependency is "ours" when it points at the
// reachability trigger of another host this sync owns; everything else on the trigger (the same host's own
// triggers, whatever a person added elsewhere) is kept as it is.
func PlanTopology(devices []Device, links map[string][]Link, hosts []ZbxHost, triggers []ReachTrigger,
	networkID string, vantages []string) (TopologyPlan, error) {
	out := TopologyPlan{Vantages: vantages}
	byHost := map[string]ZbxHost{}
	for _, h := range hosts {
		byHost[fold(h.Host)] = h
	}
	trigOf := map[string]ReachTrigger{} // host id -> trigger (lowest id when a host somehow has two)
	for _, t := range triggers {
		if cur, ok := trigOf[t.HostID]; !ok || idLess(t.TriggerID, cur.TriggerID) {
			trigOf[t.HostID] = t
		}
	}
	owned := func(h ZbxHost) bool {
		return h.Managed() && !h.IsNetworkHost() && h.TagMap()["fwd-network"] == networkID
	}

	sorted := append([]Device(nil), devices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	known := map[string]bool{}
	clash := map[string]int{}
	for _, d := range sorted {
		known[d.Name] = true
		clash[fold(TechnicalName(d.Name))]++
	}
	clashHost := map[string]bool{} // hosts two devices claim: left entirely alone
	for k, n := range clash {
		if n > 1 {
			clashHost[k] = true
		}
	}
	for _, v := range vantages {
		if !known[v] {
			return out, fmt.Errorf("vantage %q is not a device in this Forward network", v)
		}
	}

	type managedDev struct {
		host ZbxHost
		trig ReachTrigger
	}
	managed := map[string]managedDev{}
	var names []string
	for _, d := range sorted {
		key := fold(TechnicalName(d.Name))
		h, ok := byHost[key]
		switch {
		case clash[key] > 1:
			out.Left = append(out.Left, d.Name+": same Zabbix host name as another device")
		case !ok:
			out.Left = append(out.Left, d.Name+": no Zabbix host")
		case !owned(h):
			out.Left = append(out.Left, d.Name+": host '"+h.Host+"' is not managed by this sync")
		default:
			t, ok := trigOf[h.HostID]
			if !ok {
				out.NoTrigger = append(out.NoTrigger, d.Name)
				continue
			}
			managed[d.Name] = managedDev{h, t}
			names = append(names, d.Name)
		}
	}
	for _, v := range vantages {
		if _, ok := managed[v]; !ok {
			out.UnmanagedVantages = append(out.UnmanagedVantages, v)
		}
	}

	var rows []topology.Link
	for dev, ls := range links {
		for _, l := range ls {
			rows = append(rows, topology.Link{Device: dev, Peer: l.Peer})
		}
	}
	tree, err := topology.Build(rows, vantages, names)
	if err != nil {
		return out, err
	}
	out.Unreachable = tree.Unreachable
	out.Skipped = tree.Skipped
	cut := map[string]bool{}
	for _, n := range tree.Unreachable {
		cut[n] = true
	}
	for _, n := range names {
		if _, ok := tree.Parent[n]; !ok && !cut[n] {
			out.Roots = append(out.Roots, n)
		}
	}

	// every reachability trigger of a host this sync owns, including hosts whose device has gone: a dependency on
	// any of them is this tool's to maintain
	ownedTrig := map[string]string{} // trigger id -> host
	deviceOfHost := map[string]string{}
	for n, m := range managed {
		deviceOfHost[m.host.HostID] = n
	}
	var ownedHosts []ZbxHost
	for _, h := range hosts {
		if t, ok := trigOf[h.HostID]; ok && owned(h) && !clashHost[fold(h.Host)] {
			ownedTrig[t.TriggerID] = h.Host
			ownedHosts = append(ownedHosts, h)
		}
	}
	sort.Slice(ownedHosts, func(i, j int) bool { return ownedHosts[i].Host < ownedHosts[j].Host })

	for _, h := range ownedHosts {
		t := trigOf[h.HostID]
		dev := deviceOfHost[h.HostID]
		var keep, was []string
		for _, dep := range t.Deps {
			if host, ours := ownedTrig[dep]; ours && dep != t.TriggerID {
				was = append(was, host)
			} else {
				keep = append(keep, dep)
			}
		}
		want := append([]string(nil), keep...)
		parentHost := ""
		if p, ok := tree.Parent[dev]; ok && dev != "" {
			parentHost = managed[p].host.Host
			want = append(want, managed[p].trig.TriggerID)
		}
		if sameStrings(want, t.Deps) {
			if dev != "" {
				out.Unchanged = append(out.Unchanged, dev)
			}
			continue
		}
		sort.Strings(want)
		sort.Strings(was)
		out.Change = append(out.Change, DepChange{Device: dev, Host: h.Host, Was: was, Parent: parentHost,
			TriggerID: t.TriggerID, Keep: keep, Want: want})
	}
	sort.Strings(out.Unchanged)
	return out, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// Normalized returns the plan with empty lists in place of nil ones, so JSON output has a stable shape.
func (p TopologyPlan) Normalized() TopologyPlan {
	for _, l := range []*[]string{&p.Vantages, &p.Unchanged, &p.Roots, &p.Unreachable, &p.NoTrigger, &p.Left, &p.UnmanagedVantages} {
		if *l == nil {
			*l = []string{}
		}
	}
	if p.Change == nil {
		p.Change = []DepChange{}
	}
	for i := range p.Change {
		if p.Change[i].Was == nil {
			p.Change[i].Was = []string{}
		}
	}
	if p.Skipped == nil {
		p.Skipped = map[string][]string{}
	}
	return p
}

// idLess orders Zabbix ids numerically ("999" before "1000"); ids are decimal strings.
func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
