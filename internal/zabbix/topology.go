package zabbix

import (
	"context"
	"fmt"

	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

// ReachabilityTriggers lists the reachability trigger of every host that has one, with the triggers it depends on.
// The trigger is the one the Forward Device template defines, found by its fwd-role tag. Disabled hosts and
// triggers are included: a dependency on one still belongs to this tool and must be removable.
func (c *Client) ReachabilityTriggers(ctx context.Context) ([]plan.ReachTrigger, error) {
	var rows []struct {
		TriggerID string `json:"triggerid"`
		Hosts     []struct {
			HostID string `json:"hostid"`
		} `json:"hosts"`
		Dependencies []struct {
			TriggerID string `json:"triggerid"`
		} `json:"dependencies"`
	}
	err := c.Call(ctx, "trigger.get", map[string]any{
		"output": []string{"triggerid"}, "selectHosts": []string{"hostid"}, "selectDependencies": []string{"triggerid"},
		"tags":     []map[string]any{{"tag": plan.ReachTag, "value": plan.ReachValue, "operator": 1}},
		"evaltype": 0, "filter": map[string]any{"flags": 0}, "templated": false}, &rows)
	if err != nil {
		return nil, err
	}
	var out []plan.ReachTrigger
	for _, r := range rows {
		if len(r.Hosts) != 1 {
			continue
		}
		t := plan.ReachTrigger{HostID: r.Hosts[0].HostID, TriggerID: r.TriggerID}
		for _, d := range r.Dependencies {
			t.Deps = append(t.Deps, d.TriggerID)
		}
		out = append(out, t)
	}
	return out, nil
}

// AppliedTopology counts what ApplyTopology did.
type AppliedTopology struct {
	Updated  int      `json:"updated"`
	Failures []string `json:"failures"`
}

// ApplyTopology sets the dependency lists in two passes: first every changed trigger drops what it depends on
// through this tool, then the wanted dependencies go in. Doing the removals first means a moved vantage, which turns
// edges around, never asks Zabbix for a dependency that would briefly close a loop. A failure on one host is
// recorded and the rest carry on; a host whose removal failed does not get its additions.
func (c *Client) ApplyTopology(ctx context.Context, p plan.TopologyPlan) (AppliedTopology, error) {
	a := AppliedTopology{Failures: []string{}}
	set := func(triggerID string, deps []string) error {
		list := make([]map[string]string, len(deps))
		for i, d := range deps {
			list[i] = map[string]string{"triggerid": d}
		}
		return c.Call(ctx, "trigger.update", map[string]any{"triggerid": triggerID, "dependencies": list}, nil)
	}
	failed := map[string]bool{}
	for _, ch := range p.Change {
		if len(ch.Was) == 0 {
			continue
		}
		if err := set(ch.TriggerID, ch.Keep); err != nil {
			failed[ch.Host] = true
			a.Failures = append(a.Failures, fmt.Sprintf("%s: %v", ch.Host, err))
		}
	}
	for _, ch := range p.Change {
		if failed[ch.Host] {
			continue
		}
		if ch.Parent != "" {
			if err := set(ch.TriggerID, ch.Want); err != nil {
				failed[ch.Host] = true
				a.Failures = append(a.Failures, fmt.Sprintf("%s: %v", ch.Host, err))
				continue
			}
		}
		a.Updated++
	}
	return a, nil
}
