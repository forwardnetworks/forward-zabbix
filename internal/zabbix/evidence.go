package zabbix

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Maintenance is a Zabbix maintenance period and the hosts it covers.
type Maintenance struct {
	Name  string
	Since time.Time
	Till  time.Time
	Hosts []string // technical host names, those named directly and those in its host groups
}

// Maintenance reads one maintenance by exact name. Its active period is used as the change window; a maintenance with
// recurring time periods has a longer active period than any one window, so callers can override the times.
func (c *Client) Maintenance(ctx context.Context, name string) (Maintenance, error) {
	var rows []struct {
		Name        string                  `json:"name"`
		ActiveSince string                  `json:"active_since"`
		ActiveTill  string                  `json:"active_till"`
		Hosts       []struct{ Host string } `json:"hosts"`
		HostGroups  []struct {
			GroupID string `json:"groupid"`
		} `json:"hostgroups"`
	}
	err := c.Call(ctx, "maintenance.get", map[string]any{"output": []string{"name", "active_since", "active_till"},
		"filter": map[string]any{"name": name}, "selectHosts": []string{"host"}, "selectHostGroups": []string{"groupid"}}, &rows)
	if err != nil {
		return Maintenance{}, err
	}
	if len(rows) != 1 {
		return Maintenance{}, fmt.Errorf("zabbix has %d maintenance periods named %q, want exactly one", len(rows), name)
	}
	r := rows[0]
	m := Maintenance{Name: r.Name}
	since, _ := strconv.ParseInt(r.ActiveSince, 10, 64)
	till, _ := strconv.ParseInt(r.ActiveTill, 10, 64)
	m.Since, m.Till = time.Unix(since, 0).UTC(), time.Unix(till, 0).UTC()
	seen := map[string]bool{}
	for _, h := range r.Hosts {
		seen[h.Host] = true
	}
	if len(r.HostGroups) > 0 {
		ids := make([]string, len(r.HostGroups))
		for i, g := range r.HostGroups {
			ids[i] = g.GroupID
		}
		var hosts []struct{ Host string }
		if err := c.Call(ctx, "host.get", map[string]any{"output": []string{"host"}, "groupids": ids}, &hosts); err != nil {
			return Maintenance{}, err
		}
		for _, h := range hosts {
			seen[h.Host] = true
		}
	}
	for h := range seen {
		m.Hosts = append(m.Hosts, h)
	}
	sort.Strings(m.Hosts)
	return m, nil
}

// WindowProblem is a problem that opened inside a change window.
type WindowProblem struct {
	EventID    string    `json:"eventid"`
	Host       string    `json:"host"`
	Name       string    `json:"name"`
	Severity   string    `json:"severity"`
	Opened     time.Time `json:"opened"`
	Resolved   bool      `json:"resolved"`   // had recovered by the time of the query
	Suppressed bool      `json:"suppressed"` // held back by a maintenance that is still in effect when asked; Zabbix drops the mark afterwards
}

// ProblemsBetween lists the trigger problems that opened with an event time in [from, to]. It reads events, not the
// problem list: the problem list holds only what is still open, and a problem that opened during a change and recovered
// is exactly what a change leaves behind. hostIDs limits it to those hosts. Suppressed ones are included too: a
// maintenance hides them from the on-call view, but they are still evidence of what the change did.
func (c *Client) ProblemsBetween(ctx context.Context, from, to time.Time, hostIDs []string) ([]WindowProblem, error) {
	if len(hostIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		EventID    string `json:"eventid"`
		Name       string `json:"name"`
		Clock      string `json:"clock"`
		Severity   string `json:"severity"`
		ObjectID   string `json:"objectid"`
		Recovery   string `json:"r_eventid"`
		Suppressed string `json:"suppressed"`
	}
	err := c.Call(ctx, "event.get", map[string]any{"output": []string{"eventid", "name", "clock", "severity", "objectid", "r_eventid", "suppressed"},
		"source": 0, "object": 0, "value": 1, "hostids": hostIDs, "time_from": from.Unix(), "time_till": to.Unix(),
		"sortfield": []string{"eventid"}, "sortorder": "ASC"}, &rows)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	pr := make([]problemRow, len(rows))
	for i, r := range rows {
		pr[i] = problemRow{ObjectID: r.ObjectID}
	}
	hostOf, err := c.triggerHostsWhere(ctx, pr, false) // a host or trigger disabled after the change still counts
	if err != nil {
		return nil, err
	}
	var out []WindowProblem
	for _, r := range rows {
		clock, _ := strconv.ParseInt(r.Clock, 10, 64)
		out = append(out, WindowProblem{EventID: r.EventID, Host: hostOf[r.ObjectID], Name: r.Name, Severity: r.Severity,
			Opened: time.Unix(clock, 0).UTC(), Resolved: r.Recovery != "" && r.Recovery != "0", Suppressed: r.Suppressed == "1"})
	}
	return out, nil
}
