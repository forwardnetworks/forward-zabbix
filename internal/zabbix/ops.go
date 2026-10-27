package zabbix

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

var snmpDetails = map[string]any{"version": 2, "bulk": 1, "community": "{$SNMP_COMMUNITY}"}

type hostRow struct {
	HostID     string                        `json:"hostid"`
	Host       string                        `json:"host"`
	Name       string                        `json:"name"`
	Tags       []struct{ Tag, Value string } `json:"tags"`
	Interfaces []struct {
		InterfaceID string `json:"interfaceid"`
		IP          string `json:"ip"`
		Main        string `json:"main"`
	} `json:"interfaces"`
	HostGroups      []struct{ Name string } `json:"hostgroups"`
	ParentTemplates []struct{ Host string } `json:"parentTemplates"`
}

// ListHosts returns every Zabbix host as the planner sees it.
func (c *Client) ListHosts(ctx context.Context) ([]plan.ZbxHost, error) {
	var rows []hostRow
	err := c.Call(ctx, "host.get", map[string]any{
		"output": []string{"hostid", "host", "name"}, "selectTags": []string{"tag", "value"},
		"selectInterfaces": []string{"interfaceid", "ip", "main"}, "selectHostGroups": []string{"name"},
		"selectParentTemplates": []string{"host"}}, &rows)
	if err != nil {
		return nil, err
	}
	hosts := make([]plan.ZbxHost, 0, len(rows))
	for _, r := range rows {
		h := plan.ZbxHost{HostID: r.HostID, Host: r.Host, Name: r.Name, Groups: map[string]bool{}, Templates: map[string]bool{}}
		sort.SliceStable(r.Interfaces, func(i, j int) bool { return r.Interfaces[i].Main == "1" && r.Interfaces[j].Main != "1" })
		for _, i := range r.Interfaces {
			if i.IP != "" {
				h.IPs = append(h.IPs, i.IP)
			}
		}
		if len(r.Interfaces) > 0 {
			h.InterfaceID = r.Interfaces[0].InterfaceID
		}
		for _, g := range r.HostGroups {
			h.Groups[g.Name] = true
		}
		for _, t := range r.Tags {
			h.Tags = append(h.Tags, plan.Tag{Tag: t.Tag, Value: t.Value})
		}
		for _, t := range r.ParentTemplates {
			h.Templates[t.Host] = true
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Client) groupIDs(ctx context.Context, names map[string]bool) (map[string]string, error) {
	var got []struct {
		GroupID string `json:"groupid"`
		Name    string `json:"name"`
	}
	if err := c.Call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid", "name"},
		"filter": map[string]any{"name": sortedKeys(names)}}, &got); err != nil {
		return nil, err
	}
	have := map[string]string{}
	for _, g := range got {
		have[g.Name] = g.GroupID
	}
	for _, name := range sortedKeys(names) {
		if _, ok := have[name]; ok {
			continue
		}
		var made struct {
			GroupIDs []string `json:"groupids"`
		}
		if err := c.Call(ctx, "hostgroup.create", map[string]any{"name": name}, &made); err != nil {
			return nil, err
		}
		have[name] = made.GroupIDs[0]
	}
	return have, nil
}

func (c *Client) templateIDs(ctx context.Context, names map[string]bool) (map[string]string, error) {
	var got []struct {
		TemplateID string `json:"templateid"`
		Host       string `json:"host"`
	}
	if err := c.Call(ctx, "template.get", map[string]any{"output": []string{"templateid", "host"},
		"filter": map[string]any{"host": sortedKeys(names)}}, &got); err != nil {
		return nil, err
	}
	have := map[string]string{}
	for _, t := range got {
		have[t.Host] = t.TemplateID
	}
	var missing []string
	for _, n := range sortedKeys(names) {
		if _, ok := have[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Zabbix template(s) not found: %s; import zabbix/forward-templates.yaml", strings.Join(missing, ", "))
	}
	return have, nil
}

func snmpInterface(ip string) map[string]any {
	return map[string]any{"type": 2, "main": 1, "useip": 1, "ip": ip, "dns": "", "port": "161", "details": snmpDetails}
}

func tagParams(tags []plan.Tag) []map[string]string {
	out := make([]map[string]string, len(tags))
	for i, t := range tags {
		out[i] = map[string]string{"tag": t.Tag, "value": t.Value}
	}
	return out
}

func idList(key string, names []string, ids map[string]string) []map[string]string {
	out := make([]map[string]string, len(names))
	for i, n := range names {
		out[i] = map[string]string{key: ids[n]}
	}
	return out
}

// Applied counts what ApplyPlan did.
type Applied struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
}

// ApplyPlan creates and updates only. Stale and colliding hosts are never touched.
func (c *Client) ApplyPlan(ctx context.Context, p plan.SyncPlan) (Applied, error) {
	groups, templates := map[string]bool{}, map[string]bool{}
	for _, d := range p.Create {
		for g := range d.Groups {
			groups[g] = true
		}
		for t := range d.Templates {
			templates[t] = true
		}
	}
	for _, u := range p.Update {
		for g := range u.Want.Groups {
			groups[g] = true
		}
		for t := range u.Want.Templates {
			templates[t] = true
		}
		for t := range u.Existing.Templates {
			templates[t] = true
		}
	}
	var gid, tid map[string]string
	var err error
	if len(groups) > 0 {
		if gid, err = c.groupIDs(ctx, groups); err != nil {
			return Applied{}, err
		}
	}
	if len(templates) > 0 {
		if tid, err = c.templateIDs(ctx, templates); err != nil {
			return Applied{}, err
		}
	}
	for _, d := range p.Create {
		err := c.Call(ctx, "host.create", map[string]any{"host": d.Host, "name": d.Name,
			"interfaces": []any{snmpInterface(d.IP)}, "groups": idList("groupid", sortedKeys(d.Groups), gid),
			"templates": idList("templateid", sortedKeys(d.Templates), tid), "tags": tagParams(d.Tags)}, nil)
		if err != nil {
			return Applied{}, err
		}
	}
	for _, u := range p.Update {
		linked := map[string]bool{}
		for t := range u.Existing.Templates {
			linked[t] = true
		}
		for t := range u.Want.Templates {
			linked[t] = true
		}
		params := map[string]any{"hostid": u.Existing.HostID, "name": u.Want.Name,
			"groups": idList("groupid", sortedKeys(u.Want.Groups), gid), "tags": tagParams(u.Want.Tags),
			// host.update replaces the template list, so send what is already linked plus what is missing
			"templates": idList("templateid", sortedKeys(linked), tid)}
		hasIP := false
		for _, ip := range u.Existing.IPs {
			hasIP = hasIP || ip == u.Want.IP
		}
		if !hasIP && u.Existing.InterfaceID != "" {
			i := snmpInterface(u.Want.IP)
			i["interfaceid"] = u.Existing.InterfaceID
			params["interfaces"] = []any{i}
		}
		if err := c.Call(ctx, "host.update", params, nil); err != nil {
			return Applied{}, err
		}
	}
	return Applied{len(p.Create), len(p.Update)}, nil
}

// EnsureNetworkHost creates the host that carries network-level trapper items. A same-named host this tool
// does not own is an error.
func (c *Client) EnsureNetworkHost(ctx context.Context, networkID string) (string, error) {
	name := plan.NetworkHostName(networkID)
	var found []struct {
		HostID string                        `json:"hostid"`
		Tags   []struct{ Tag, Value string } `json:"tags"`
	}
	if err := c.Call(ctx, "host.get", map[string]any{"output": []string{"hostid"},
		"filter": map[string]any{"host": []string{name}}, "selectTags": []string{"tag", "value"}}, &found); err != nil {
		return "", err
	}
	if len(found) > 0 {
		for _, t := range found[0].Tags {
			if t.Tag == "managed-by" && t.Value == plan.ManagedBy {
				return name, nil
			}
		}
		return "", fmt.Errorf("host '%s' exists and is not managed by fwdzbx; refusing to write to it", name)
	}
	gid, err := c.groupIDs(ctx, map[string]bool{plan.RootGroup: true})
	if err != nil {
		return "", err
	}
	tid, err := c.templateIDs(ctx, map[string]bool{plan.NetworkTemplate: true})
	if err != nil {
		return "", err
	}
	err = c.Call(ctx, "host.create", map[string]any{"host": name, "name": "Forward network " + networkID,
		"groups":    []map[string]string{{"groupid": gid[plan.RootGroup]}},
		"templates": []map[string]string{{"templateid": tid[plan.NetworkTemplate]}},
		"tags":      []map[string]string{{"tag": "managed-by", "value": plan.ManagedBy}, {"tag": "fwd-network", "value": networkID}}}, nil)
	return name, err
}

// ExistingTargets returns the (host, key) pairs that have an item in Zabbix. The trapper protocol only
// returns counts, so this is how a push can name what it skipped.
func (c *Client) ExistingTargets(ctx context.Context, values []Value) (map[[2]string]bool, error) {
	keys := map[string]bool{}
	for _, v := range values {
		keys[v.Key] = true
	}
	var rows []struct {
		Key   string                  `json:"key_"`
		Hosts []struct{ Host string } `json:"hosts"`
	}
	if err := c.Call(ctx, "item.get", map[string]any{"output": []string{"key_"}, "selectHosts": []string{"host"},
		"filter": map[string]any{"key_": sortedKeys(keys)}, "templated": false}, &rows); err != nil {
		return nil, err
	}
	have := map[[2]string]bool{}
	for _, r := range rows {
		for _, h := range r.Hosts {
			have[[2]string{h.Host, r.Key}] = true
		}
	}
	return have, nil
}

// Problem is an open Zabbix problem on a managed host.
type Problem struct {
	EventID string `json:"eventid"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Noted   bool   `json:"noted"` // already has a Forward note
}

type problemRow struct {
	EventID      string                     `json:"eventid"`
	Name         string                     `json:"name"`
	ObjectID     string                     `json:"objectid"`
	Severity     string                     `json:"severity"`
	Acknowledges []struct{ Message string } `json:"acknowledges"`
}

func (c *Client) triggerHosts(ctx context.Context, rows []problemRow) (map[string]string, error) {
	return c.triggerHostsWhere(ctx, rows, true)
}

// triggerHostsWhere maps trigger ids to host names; monitoredOnly leaves out triggers on disabled hosts or that are
// disabled themselves.
func (c *Client) triggerHostsWhere(ctx context.Context, rows []problemRow, monitoredOnly bool) (map[string]string, error) {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ObjectID
	}
	var trig []struct {
		TriggerID string                  `json:"triggerid"`
		Hosts     []struct{ Host string } `json:"hosts"`
	}
	params := map[string]any{"output": []string{"triggerid"}, "triggerids": ids, "selectHosts": []string{"host"}}
	if monitoredOnly {
		params["monitored"] = true
	}
	if err := c.Call(ctx, "trigger.get", params, &trig); err != nil {
		return nil, err
	}
	hostOf := map[string]string{}
	for _, t := range trig {
		if len(t.Hosts) > 0 {
			hostOf[t.TriggerID] = t.Hosts[0].Host
		}
	}
	return hostOf, nil
}

// OpenProblems lists open, unsuppressed problems on hosts this tool manages.
func (c *Client) OpenProblems(ctx context.Context) ([]Problem, error) {
	var rows []problemRow
	if err := c.Call(ctx, "problem.get", map[string]any{"output": []string{"eventid", "name", "objectid"},
		"selectAcknowledges": []string{"message"}, "suppressed": false}, &rows); err != nil || len(rows) == 0 {
		return nil, err
	}
	hostOf, err := c.triggerHosts(ctx, rows)
	if err != nil {
		return nil, err
	}
	var managedRows []struct{ Host string }
	if err := c.Call(ctx, "host.get", map[string]any{"output": []string{"host"},
		"tags": []map[string]string{{"tag": "managed-by", "value": plan.ManagedBy}}}, &managedRows); err != nil {
		return nil, err
	}
	managed := map[string]bool{}
	for _, h := range managedRows {
		managed[h.Host] = true
	}
	var out []Problem
	for _, r := range rows {
		host, ok := hostOf[r.ObjectID]
		if !ok || !managed[host] {
			continue
		}
		noted := false
		for _, a := range r.Acknowledges {
			noted = noted || strings.HasPrefix(a.Message, plan.NotePrefix)
		}
		out = append(out, Problem{r.EventID, r.Name, host, noted})
	}
	return out, nil
}

// AddNote adds a message to a problem without acknowledging it (action 4 = add message only).
func (c *Client) AddNote(ctx context.Context, eventID, message string) error {
	return c.Call(ctx, "event.acknowledge", map[string]any{"eventids": []string{eventID}, "action": 4, "message": message}, nil)
}

// ProblemCounts returns open, unsuppressed problems per monitored host (casefolded name).
func (c *Client) ProblemCounts(ctx context.Context) (map[string]plan.ProblemCount, error) {
	var rows []problemRow
	if err := c.Call(ctx, "problem.get", map[string]any{"output": []string{"objectid", "severity"}, "suppressed": false}, &rows); err != nil {
		return nil, err
	}
	out := map[string]plan.ProblemCount{}
	if len(rows) == 0 {
		return out, nil
	}
	hostOf, err := c.triggerHosts(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		host, ok := hostOf[r.ObjectID]
		if !ok {
			continue
		}
		sev, _ := strconv.Atoi(r.Severity)
		pc := out[strings.ToLower(host)]
		pc.Count++
		if sev > pc.Worst {
			pc.Worst = sev
		}
		out[strings.ToLower(host)] = pc
	}
	return out, nil
}
