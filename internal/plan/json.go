package plan

import (
	"encoding/json"
	"sort"
)

// The JSON shapes below are the machine-readable contract of `fwdzbx --json`; they match the original Python
// implementation, so pairs are arrays and sets are sorted lists, and empty lists are [] rather than null.

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nz[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func pairs(tags []Tag) [][2]string {
	out := make([][2]string, len(tags))
	for i, t := range tags {
		out[i] = [2]string{t.Tag, t.Value}
	}
	return out
}

func (d Device) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"name": d.Name, "site": d.Site, "vendor": d.Vendor, "model": d.Model, "os": d.OS,
		"os_version": d.OSVersion, "device_type": d.DeviceType, "ips": nz(d.IPs), "tags": nz(d.Tags), "collected": d.Collected})
}

func (l Link) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"iface": l.Iface, "peer": l.Peer, "peer_iface": l.PeerIface})
}

func (h ZbxHost) MarshalJSON() ([]byte, error) {
	var iface any
	if h.InterfaceID != "" {
		iface = h.InterfaceID
	}
	return json.Marshal(map[string]any{"hostid": h.HostID, "host": h.Host, "name": h.Name, "ips": nz(h.IPs),
		"groups": keys(h.Groups), "tags": pairs(h.Tags), "interfaceid": iface, "templates": keys(h.Templates)})
}

func (d Desired) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"host": d.Host, "name": d.Name, "ip": d.IP, "groups": keys(d.Groups),
		"tags": pairs(d.Tags), "templates": keys(d.Templates)})
}

func (u Update) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{u.Existing, u.Want, nz(u.Changes)})
}

func (c Collision) MarshalJSON() ([]byte, error) { return json.Marshal([2]string{c.Device, c.Reason}) }
func (d Drift) MarshalJSON() ([]byte, error)     { return json.Marshal([2]string{d.Device, d.What}) }
func (v Value) MarshalJSON() ([]byte, error)     { return json.Marshal([3]string{v.Host, v.Key, v.Value}) }

func (p SyncPlan) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"create": nz(p.Create), "update": nz(p.Update), "unchanged": nz(p.Unchanged),
		"collisions": nz(p.Collisions), "stale": nz(p.Stale), "skipped_no_ip": nz(p.SkippedNoIP)})
}

func (g Gap) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"unmonitored": nz(g.Unmonitored), "unmodeled": nz(g.Unmodeled),
		"drift": nz(g.Drift), "matched": g.Matched})
}
