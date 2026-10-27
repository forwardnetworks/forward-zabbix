package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/forwardnetworks/forward-zabbix/internal/topology"
)

// Kinds of service this tool creates. Zabbix does not allow a service to have both problem tags and children, so a
// device is two services: a container, which holds the devices behind it, and a leaf under it that carries the
// problem tags and so shows the device's own problems.
const (
	SvcRoot   = "network"
	SvcDevice = "device"
	SvcSelf   = "self"
)

// Svc is a Zabbix service as the planner sees it.
type Svc struct {
	ID          string
	Name        string
	Algorithm   string // Zabbix status algorithm; this tool wants "2", most critical of the children
	Tags        []Tag
	ProblemTags []Tag
	ParentIDs   []string
}

// SvcAlgorithm is Zabbix's "most critical of child services", the rule that makes a status the worst one beneath it.
const SvcAlgorithm = "2"

func (s Svc) tagMap() map[string]string {
	m := make(map[string]string, len(s.Tags))
	for _, t := range s.Tags {
		m[t.Tag] = t.Value
	}
	return m
}

// Key is what identifies a service this tool made: its kind and device, from its tags (names are not unique in
// Zabbix). It is empty for a service that is not this tool's.
func (s Svc) Key(networkID string) string {
	m := s.tagMap()
	if m["managed-by"] != ManagedBy || m["fwd-network"] != networkID || m["fwd-service"] == "" {
		return ""
	}
	return m["fwd-service"] + "|" + m["fwd-device"]
}

// SvcWant is a service that should exist.
type SvcWant struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Device      string `json:"device"`
	Name        string `json:"name"`
	ParentKey   string `json:"parent"` // empty for the network root
	Tags        []Tag  `json:"-"`
	ProblemTags []Tag  `json:"-"`
}

// SvcUpdate is an existing service of this tool that differs from what it should be.
type SvcUpdate struct {
	ID          string   `json:"id"`
	Want        SvcWant  `json:"want"`
	Changes     []string `json:"changes"`
	KeepParents []string `json:"-"` // parents this tool does not own, kept as they are
}

// ServicesPlan is what a service-tree sync would do. It creates and updates only; it never deletes.
type ServicesPlan struct {
	Vantages    []string          `json:"vantages"`
	Create      []SvcWant         `json:"create"` // parents before children
	Update      []SvcUpdate       `json:"update"`
	Unchanged   []string          `json:"unchanged"`
	Skipped     []string          `json:"skipped"`     // devices left out, with the reason
	Unreachable []string          `json:"unreachable"` // no link path to a vantage: shown directly under the network
	Stale       []string          `json:"stale"`       // this tool's services whose device is gone; never deleted
	Duplicates  []string          `json:"duplicates"`  // two services claim the same device and kind (a cloned service?): left alone
	IDs         map[string]string `json:"-"`           // key -> id of the services that exist now
}

// PlanServices works out the service tree: one root for the network, and for each device a container under the
// device it sits behind (as in PlanTopology) plus a leaf under that container with the device's own problems. A
// device qualifies when its Zabbix host is this sync's and carries the fwd-device tag; otherwise a service for it
// would never match a problem and look healthy while the device was down.
func PlanServices(devices []Device, links map[string][]Link, hosts []ZbxHost, existing []Svc, networkID string, vantages []string) (ServicesPlan, error) {
	out := ServicesPlan{Vantages: vantages, IDs: map[string]string{}}
	byHost := map[string]ZbxHost{}
	for _, h := range hosts {
		byHost[fold(h.Host)] = h
	}
	sorted := append([]Device(nil), devices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	known := map[string]bool{}
	clash := map[string]int{}
	for _, d := range sorted {
		known[d.Name] = true
		clash[fold(TechnicalName(d.Name))]++
	}
	for _, v := range vantages {
		if !known[v] {
			return out, fmt.Errorf("vantage %q is not a device in this Forward network", v)
		}
	}
	var names []string
	for _, d := range sorted {
		key := fold(TechnicalName(d.Name))
		h, ok := byHost[key]
		switch {
		case clash[key] > 1:
			out.Skipped = append(out.Skipped, d.Name+": same Zabbix host name as another device")
		case !ok:
			out.Skipped = append(out.Skipped, d.Name+": no Zabbix host")
		case !h.Managed() || h.IsNetworkHost() || h.TagMap()["fwd-network"] != networkID:
			out.Skipped = append(out.Skipped, d.Name+": host '"+h.Host+"' is not managed by this sync")
		case h.TagMap()["fwd-device"] != d.Name:
			out.Skipped = append(out.Skipped, d.Name+": host has no fwd-device tag; run sync-inventory --apply first")
		default:
			names = append(names, d.Name)
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

	rootKey := SvcRoot + "|"
	tagsFor := func(kind, device string) []Tag {
		t := []Tag{{"managed-by", ManagedBy}, {"fwd-network", networkID}, {"fwd-service", kind}}
		if device != "" {
			t = append(t, Tag{"fwd-device", device})
		}
		sortTags(t)
		return t
	}
	wants := []SvcWant{{Key: rootKey, Kind: SvcRoot, Name: "Forward network " + networkID, Tags: tagsFor(SvcRoot, "")}}
	depth := func(n string) int {
		d := 0
		for p, ok := tree.Parent[n]; ok; p, ok = tree.Parent[p] {
			d++
		}
		return d
	}
	order := append([]string(nil), names...)
	sort.SliceStable(order, func(i, j int) bool { return depth(order[i]) < depth(order[j]) })
	for _, n := range order {
		parent := rootKey
		if p, ok := tree.Parent[n]; ok {
			parent = SvcDevice + "|" + p
		}
		wants = append(wants,
			SvcWant{Key: SvcDevice + "|" + n, Kind: SvcDevice, Device: n, Name: n, ParentKey: parent, Tags: tagsFor(SvcDevice, n)},
			SvcWant{Key: SvcSelf + "|" + n, Kind: SvcSelf, Device: n, Name: n + " (device itself)", ParentKey: SvcDevice + "|" + n,
				Tags: tagsFor(SvcSelf, n), ProblemTags: []Tag{{"fwd-device", n}}})
	}

	have := map[string]Svc{}
	idKey := map[string]string{} // id -> key, for the services that are this tool's
	dup := map[string]bool{}
	for _, s := range existing {
		if k := s.Key(networkID); k != "" {
			idKey[s.ID] = k
			if _, seen := have[k]; seen {
				dup[k] = true
				continue
			}
			have[k] = s
			out.IDs[k] = s.ID
		}
	}
	for k := range dup {
		out.Duplicates = append(out.Duplicates, k)
		delete(out.IDs, k)
	}
	sort.Strings(out.Duplicates)
	wanted := map[string]bool{}
	for _, w := range wants {
		wanted[w.Key] = true
		if dup[w.Key] {
			continue // which one to keep is a person's call
		}
		s, ok := have[w.Key]
		if !ok {
			out.Create = append(out.Create, w)
			continue
		}
		var changes []string
		if s.Name != w.Name {
			changes = append(changes, fmt.Sprintf("name %q -> %q", s.Name, w.Name))
		}
		if s.Algorithm != SvcAlgorithm {
			changes = append(changes, "status rule")
		}
		if !sameTags(s.ProblemTags, w.ProblemTags) {
			changes = append(changes, "problem tags")
		}
		var ours, keep []string
		for _, id := range s.ParentIDs {
			if _, mine := idKey[id]; mine {
				ours = append(ours, id)
			} else {
				keep = append(keep, id)
			}
		}
		parentLeftAlone := dup[w.ParentKey]
		if parentLeftAlone {
			// which of the duplicate parents is meant is a person's call, so leave this service's parent as it is
		} else if w.ParentKey != "" {
			if pid, ok := out.IDs[w.ParentKey]; !ok || len(ours) != 1 || ours[0] != pid {
				changes = append(changes, "parent -> "+strings.TrimPrefix(w.ParentKey, SvcDevice+"|"))
			}
		} else if len(ours) > 0 {
			changes = append(changes, "parent removed")
		}
		if len(changes) == 0 {
			out.Unchanged = append(out.Unchanged, w.Key)
			continue
		}
		if parentLeftAlone {
			// an update replaces the parent list, so resend today's parents unchanged
			w.ParentKey, keep = "", s.ParentIDs
		}
		out.Update = append(out.Update, SvcUpdate{ID: s.ID, Want: w, Changes: changes, KeepParents: keep})
	}
	for k, s := range have {
		if !wanted[k] && !dup[k] {
			out.Stale = append(out.Stale, s.Name)
		}
	}
	sort.Strings(out.Stale)
	sort.Strings(out.Unchanged)
	return out, nil
}

func sameTags(a, b []Tag) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[Tag]int{}
	for _, t := range a {
		m[t]++
	}
	for _, t := range b {
		m[t]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// Normalized returns the plan with empty lists in place of nil ones, so JSON output has a stable shape.
func (p ServicesPlan) Normalized() ServicesPlan {
	for _, l := range []*[]string{&p.Vantages, &p.Unchanged, &p.Skipped, &p.Unreachable, &p.Stale, &p.Duplicates} {
		if *l == nil {
			*l = []string{}
		}
	}
	if p.Create == nil {
		p.Create = []SvcWant{}
	}
	if p.Update == nil {
		p.Update = []SvcUpdate{}
	}
	return p
}
