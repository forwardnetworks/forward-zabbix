package plan

import (
	"fmt"
	"sort"
	"strings"
)

const (
	ManagedBy         = "forward-zabbix"
	RootGroup         = "Forward"
	DeviceTemplate    = "Forward Device"
	NetworkTemplate   = "Forward Network"
	NetworkHostPrefix = "forward-network-"
	NotePrefix        = "[Forward]"
	noteLimit         = 1900 // Zabbix acknowledgement messages are capped at 2048 characters
)

// TechnicalName maps a device name to the character set Zabbix allows in a technical host name.
func TechnicalName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_', r == '.', r == ' ', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func fold(s string) string { return strings.ToLower(s) }

// Tag is a Zabbix host tag.
type Tag struct{ Tag, Value string }

// ZbxHost is a Zabbix host as the planner sees it.
type ZbxHost struct {
	HostID      string
	Host        string
	Name        string
	IPs         []string
	Groups      map[string]bool
	Tags        []Tag
	InterfaceID string
	Templates   map[string]bool
}

func (h ZbxHost) TagMap() map[string]string {
	m := make(map[string]string, len(h.Tags))
	for _, t := range h.Tags {
		m[t.Tag] = t.Value
	}
	return m
}

func (h ZbxHost) Managed() bool { return h.TagMap()["managed-by"] == ManagedBy }

// IsNetworkHost reports the host this tool creates to carry network-level values; it is bookkeeping, not a device.
func (h ZbxHost) IsNetworkHost() bool {
	return h.Managed() && strings.HasPrefix(h.Host, NetworkHostPrefix)
}

// Desired is the host a Forward device should become in Zabbix.
type Desired struct {
	Host      string          `json:"host"`
	Name      string          `json:"name"`
	IP        string          `json:"ip"`
	Groups    map[string]bool `json:"-"`
	Tags      []Tag           `json:"-"`
	Templates map[string]bool `json:"-"`
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func sortTags(tags []Tag) {
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Tag != tags[j].Tag {
			return tags[i].Tag < tags[j].Tag
		}
		return tags[i].Value < tags[j].Value
	})
}

// DesiredHost returns the host for a device, or nil when the device has no management IP.
func DesiredHost(d Device, networkID string) *Desired {
	if len(d.IPs) == 0 {
		return nil
	}
	groups := set(RootGroup)
	if d.Site != "" {
		groups[RootGroup+"/Sites/"+d.Site] = true
	}
	if d.Vendor != "" {
		groups[RootGroup+"/Vendors/"+d.Vendor] = true
	}
	tags := []Tag{{"managed-by", ManagedBy}, {"fwd-network", networkID}}
	for _, kv := range [][2]string{{"fwd-site", d.Site}, {"fwd-vendor", d.Vendor}, {"fwd-model", d.Model},
		{"fwd-os", d.OS}, {"fwd-type", d.DeviceType}} {
		if kv[1] != "" {
			tags = append(tags, Tag{kv[0], kv[1]})
		}
	}
	for _, t := range d.Tags {
		tags = append(tags, Tag{"fwd-tag", t})
	}
	sortTags(tags)
	return &Desired{Host: TechnicalName(d.Name), Name: d.Name, IP: d.IPs[0], Groups: groups, Tags: tags,
		Templates: set(DeviceTemplate)}
}

// Update is one managed host that differs from what Forward implies.
type Update struct {
	Existing ZbxHost
	Want     Desired
	Changes  []string
}

// Collision is a device that cannot be created because its name or IP already belongs to another host.
type Collision struct{ Device, Reason string }

// SyncPlan is what a sync would do. It only creates and updates; it never deletes.
type SyncPlan struct {
	Create      []Desired
	Update      []Update
	Unchanged   []string
	Collisions  []Collision
	Stale       []string // managed hosts with no device; never deleted
	SkippedNoIP []string
}

func hostsByName(hosts []ZbxHost) (map[string]ZbxHost, map[string]ZbxHost) {
	byHost := map[string]ZbxHost{}
	byIP := map[string]ZbxHost{}
	for _, h := range hosts {
		byHost[fold(h.Host)] = h // a later host with the same name wins, as in the Python original
		for _, ip := range h.IPs {
			if _, ok := byIP[ip]; !ok {
				byIP[ip] = h
			}
		}
	}
	return byHost, byIP
}

// PlanSync decides which Zabbix hosts to create or update for the devices.
func PlanSync(devices []Device, hosts []ZbxHost, networkID string) SyncPlan {
	var plan SyncPlan
	byHost, byIP := hostsByName(hosts)
	wanted := map[string]bool{}
	for _, d := range devices {
		want := DesiredHost(d, networkID)
		if want == nil {
			plan.SkippedNoIP = append(plan.SkippedNoIP, d.Name)
			continue
		}
		wanted[fold(want.Host)] = true
		if existing, ok := byHost[fold(want.Host)]; ok {
			if !existing.Managed() || existing.TagMap()["fwd-network"] != networkID {
				plan.Collisions = append(plan.Collisions, Collision{d.Name,
					fmt.Sprintf("host '%s' exists and is not managed by this sync", existing.Host)})
				continue
			}
			if changes := diff(existing, *want); len(changes) > 0 {
				plan.Update = append(plan.Update, Update{existing, *want, changes})
			} else {
				plan.Unchanged = append(plan.Unchanged, d.Name)
			}
			continue
		}
		if owner, ok := byIP[want.IP]; ok {
			plan.Collisions = append(plan.Collisions, Collision{d.Name,
				fmt.Sprintf("%s is already monitored as '%s'", want.IP, owner.Host)})
			continue
		}
		plan.Create = append(plan.Create, *want)
	}
	for _, h := range hosts {
		if h.Managed() && !h.IsNetworkHost() && h.TagMap()["fwd-network"] == networkID && !wanted[fold(h.Host)] {
			plan.Stale = append(plan.Stale, h.Host)
		}
	}
	sort.Strings(plan.Stale)
	return plan
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func diff(existing ZbxHost, want Desired) []string {
	var changes []string
	if !contains(existing.IPs, want.IP) {
		cur := strings.Join(existing.IPs, ",")
		if cur == "" {
			cur = "-"
		}
		changes = append(changes, fmt.Sprintf("ip %s -> %s", cur, want.IP))
	}
	if existing.Name != want.Name {
		changes = append(changes, "visible name")
	}
	if !sameSet(existing.Groups, want.Groups) {
		changes = append(changes, "groups")
	}
	have := append([]Tag(nil), existing.Tags...)
	sortTags(have)
	if !equalTags(have, want.Tags) {
		changes = append(changes, "tags")
	}
	for t := range want.Templates { // only ever adds; never unlinks a template someone else linked
		if !existing.Templates[t] {
			changes = append(changes, "templates")
			break
		}
	}
	return changes
}

func equalTags(a, b []Tag) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

// Drift is a matched device whose Zabbix host disagrees with it.
type Drift struct{ Device, What string }

// Gap is the coverage report.
type Gap struct {
	Unmonitored []string
	Unmodeled   []string
	Drift       []Drift
	Matched     int
}

// CoverageGap diffs Forward's inventory against every Zabbix host outside the ignored groups.
func CoverageGap(devices []Device, hosts []ZbxHost, ignoreGroups ...string) Gap {
	if len(ignoreGroups) == 0 {
		ignoreGroups = []string{"Zabbix servers"}
	}
	var candidates []ZbxHost
	for _, h := range hosts {
		skip := h.IsNetworkHost()
		for _, g := range ignoreGroups {
			if h.Groups[g] {
				skip = true
			}
		}
		if !skip {
			candidates = append(candidates, h)
		}
	}
	byName := map[string]ZbxHost{}
	byIP := map[string]ZbxHost{}
	for _, h := range candidates {
		for _, n := range []string{h.Host, h.Name} {
			if _, ok := byName[fold(n)]; !ok {
				byName[fold(n)] = h
			}
		}
		for _, ip := range h.IPs {
			if _, ok := byIP[ip]; !ok {
				byIP[ip] = h
			}
		}
	}
	gap := Gap{}
	used := map[string]bool{}
	for _, d := range devices {
		host, found := byName[fold(d.Name)]
		if !found {
			host, found = byName[fold(TechnicalName(d.Name))]
		}
		by := "name"
		if !found {
			by = "ip"
			for _, ip := range d.IPs {
				if h, ok := byIP[ip]; ok {
					host, found = h, true
					break
				}
			}
		}
		if !found {
			gap.Unmonitored = append(gap.Unmonitored, d.Name)
			continue
		}
		used[host.HostID] = true
		gap.Matched++
		switch {
		case by == "ip":
			gap.Drift = append(gap.Drift, Drift{d.Name, fmt.Sprintf("monitored under a different name '%s'", host.Host)})
		case len(d.IPs) > 0 && !intersects(d.IPs, host.IPs):
			zip := strings.Join(host.IPs, ",")
			if zip == "" {
				zip = "-"
			}
			gap.Drift = append(gap.Drift, Drift{d.Name,
				fmt.Sprintf("IP differs: Forward %s vs Zabbix %s", strings.Join(d.IPs, ","), zip)})
		case host.Managed():
			if site, has := host.TagMap()["fwd-site"]; has && site != d.Site {
				gap.Drift = append(gap.Drift, Drift{d.Name,
					fmt.Sprintf("site differs: Forward '%s' vs Zabbix '%s'", d.Site, site)})
			}
		}
	}
	for _, h := range candidates {
		if !used[h.HostID] {
			gap.Unmodeled = append(gap.Unmodeled, h.Host)
		}
	}
	sort.Strings(gap.Unmodeled)
	return gap
}

func NetworkHostName(networkID string) string { return NetworkHostPrefix + networkID }

// Value is one trapper value: a host, an item key and the value to set.
type Value struct{ Host, Key, Value string }

// StatusValues returns the rows to push. A device without a collection result is skipped, not reported healthy.
func StatusValues(devices []Device, networkID, snapshotID string, snapshotEpoch int64) []Value {
	net := NetworkHostName(networkID)
	failed := 0
	for _, d := range devices {
		if d.Collected != "" && d.Collected != "completed" {
			failed++
		}
	}
	rows := []Value{{net, "fwd.devices.total", fmt.Sprint(len(devices))},
		{net, "fwd.devices.collection_failed", fmt.Sprint(failed)},
		{net, "fwd.snapshot.id", snapshotID},
		{net, "fwd.snapshot.time", fmt.Sprint(snapshotEpoch)}}
	for _, d := range devices {
		if d.Collected != "" {
			rows = append(rows, Value{TechnicalName(d.Name), "fwd.collection.result", d.Collected})
		}
	}
	return rows
}

// EnrichmentNote is the text added to a Zabbix problem: facts only, what Forward holds for the device.
func EnrichmentNote(d Device, links []Link, snapshotID string) string {
	head := strings.TrimSpace(strings.Join(nonEmpty(d.Vendor, d.Model, d.OS, d.OSVersion), " "))
	if d.Site != "" {
		head += ", site " + d.Site
	}
	if len(d.IPs) > 0 {
		head += ", mgmt " + strings.Join(d.IPs, ", ")
	}
	lines := []string{fmt.Sprintf("%s %s (snapshot %s)", NotePrefix, d.Name, snapshotID), head}
	switch {
	case d.Collected != "" && d.Collected != "completed":
		lines = append(lines, fmt.Sprintf("Forward could not collect this device (%s); its config and state in Forward are missing or stale.", d.Collected))
	case d.Collected == "completed":
		lines = append(lines, "Forward collected this device successfully in that snapshot.")
	}
	if len(links) > 0 {
		byPeer := map[string][]Link{}
		for _, l := range links {
			byPeer[l.Peer] = append(byPeer[l.Peer], l)
		}
		peers := make([]string, 0, len(byPeer))
		for p := range byPeer {
			peers = append(peers, p)
		}
		sort.Slice(peers, func(i, j int) bool { return fold(peers[i]) < fold(peers[j]) })
		lines = append(lines, fmt.Sprintf("Directly connected to %d device(s) over %d link(s):", len(peers), len(links)))
		for _, p := range peers {
			ls := byPeer[p]
			sort.Slice(ls, func(i, j int) bool { return ls[i].Iface < ls[j].Iface })
			ports := make([]string, len(ls))
			for i, l := range ls {
				ports[i] = l.Iface + "<->" + l.PeerIface
			}
			lines = append(lines, fmt.Sprintf("  %s: %s", p, strings.Join(ports, ", ")))
		}
	} else {
		lines = append(lines, "Forward models no links for this device.")
	}
	text := strings.Join(lines, "\n")
	runes := []rune(text)
	if len(runes) <= noteLimit {
		return text
	}
	return strings.TrimRight(string(runes[:noteLimit-20]), " \n") + "\n... (truncated)"
}

func nonEmpty(items ...string) []string {
	var out []string
	for _, i := range items {
		if i != "" {
			out = append(out, i)
		}
	}
	return out
}

var Severities = []string{"not classified", "information", "warning", "average", "high", "disaster"}
var StatusColumns = []string{"device", "monitored", "zabbix_host", "open_problems", "worst_severity"}

// ProblemCount is a host's open problem count and its worst severity (0-5).
type ProblemCount struct{ Count, Worst int }

// StatusRows is one row per Forward device describing what Zabbix says about it: the data NQE can join on
// `device`. problems is keyed by the casefolded Zabbix host name.
func StatusRows(devices []Device, hosts []ZbxHost, problems map[string]ProblemCount) []map[string]string {
	var candidates []ZbxHost
	for _, h := range hosts {
		if !h.IsNetworkHost() {
			candidates = append(candidates, h)
		}
	}
	byName, byIP := hostsByName(candidates)
	sorted := append([]Device(nil), devices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	rows := make([]map[string]string, 0, len(sorted))
	for _, d := range sorted {
		host, found := byName[fold(TechnicalName(d.Name))]
		if !found {
			for _, ip := range d.IPs {
				if h, ok := byIP[ip]; ok {
					host, found = h, true
					break
				}
			}
		}
		row := map[string]string{"device": d.Name, "monitored": "no", "zabbix_host": "", "open_problems": "0", "worst_severity": ""}
		if found {
			pc := problems[fold(host.Host)]
			row["monitored"], row["zabbix_host"], row["open_problems"] = "yes", host.Host, fmt.Sprint(pc.Count)
			if pc.Count > 0 {
				row["worst_severity"] = Severities[pc.Worst]
			}
		}
		rows = append(rows, row)
	}
	return rows
}
