package zabbix

import (
	"context"
	"fmt"

	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

// ListServices returns the services this tool made for a network, found by their tags (service names are not
// unique), with their parents.
func (c *Client) ListServices(ctx context.Context, networkID string) ([]plan.Svc, error) {
	type tag struct{ Tag, Value string }
	var rows []struct {
		ServiceID   string `json:"serviceid"`
		Name        string `json:"name"`
		Algorithm   string `json:"algorithm"`
		Tags        []tag  `json:"tags"`
		ProblemTags []tag  `json:"problem_tags"`
		Parents     []struct {
			ServiceID string `json:"serviceid"`
		} `json:"parents"`
	}
	err := c.Call(ctx, "service.get", map[string]any{
		"output": []string{"serviceid", "name", "algorithm"}, "selectTags": "extend", "selectProblemTags": "extend", "selectParents": []string{"serviceid"},
		"evaltype": 0, "tags": []map[string]any{
			{"tag": "managed-by", "value": plan.ManagedBy, "operator": 1},
			{"tag": "fwd-network", "value": networkID, "operator": 1}}}, &rows)
	if err != nil {
		return nil, err
	}
	out := make([]plan.Svc, 0, len(rows))
	for _, r := range rows {
		s := plan.Svc{ID: r.ServiceID, Name: r.Name, Algorithm: r.Algorithm}
		for _, t := range r.Tags {
			s.Tags = append(s.Tags, plan.Tag{Tag: t.Tag, Value: t.Value})
		}
		for _, t := range r.ProblemTags {
			s.ProblemTags = append(s.ProblemTags, plan.Tag{Tag: t.Tag, Value: t.Value})
		}
		for _, p := range r.Parents {
			s.ParentIDs = append(s.ParentIDs, p.ServiceID)
		}
		out = append(out, s)
	}
	return out, nil
}

// AppliedServices counts what ApplyServices did.
type AppliedServices struct {
	Created  int      `json:"created"`
	Updated  int      `json:"updated"`
	Failures []string `json:"failures"`
}

// tagList is the API form of tags. Only problem tags take an operator (0 = equals); Zabbix refuses it on a service's
// ordinary tags.
func tagList(tags []plan.Tag, problem bool) []map[string]any {
	out := make([]map[string]any, len(tags))
	for i, t := range tags {
		out[i] = map[string]any{"tag": t.Tag, "value": t.Value}
		if problem {
			out[i]["operator"] = 0
		}
	}
	return out
}

// ApplyServices creates the missing services parent first, then updates the changed ones. It never deletes. A
// service whose parent could not be created is skipped and reported; the rest carry on. An update replaces the
// service's parent list, so it sends the parent it should have plus the ones this tool does not own.
func (c *Client) ApplyServices(ctx context.Context, p plan.ServicesPlan) (AppliedServices, error) {
	a := AppliedServices{Failures: []string{}}
	ids := map[string]string{}
	for k, v := range p.IDs {
		ids[k] = v
	}
	parents := func(key string, keep []string) ([]map[string]string, bool) {
		out := []map[string]string{}
		if key != "" {
			id, ok := ids[key]
			if !ok {
				return nil, false
			}
			out = append(out, map[string]string{"serviceid": id})
		}
		for _, k := range keep {
			out = append(out, map[string]string{"serviceid": k})
		}
		return out, true
	}
	fail := func(name string, err error) { a.Failures = append(a.Failures, fmt.Sprintf("%s: %v", name, err)) }
	for _, w := range p.Create {
		par, ok := parents(w.ParentKey, nil)
		if !ok {
			fail(w.Name, fmt.Errorf("its parent service was not created"))
			continue
		}
		params := map[string]any{"name": w.Name, "algorithm": 2, "sortorder": 0, "tags": tagList(w.Tags, false), "parents": par}
		if len(w.ProblemTags) > 0 {
			params["problem_tags"] = tagList(w.ProblemTags, true)
		}
		var res struct {
			ServiceIDs []string `json:"serviceids"`
		}
		if err := c.Call(ctx, "service.create", params, &res); err != nil || len(res.ServiceIDs) != 1 {
			if err == nil {
				err = fmt.Errorf("no service id returned")
			}
			fail(w.Name, err)
			continue
		}
		ids[w.Key] = res.ServiceIDs[0]
		a.Created++
	}
	for _, u := range p.Update {
		par, ok := parents(u.Want.ParentKey, u.KeepParents)
		if !ok {
			fail(u.Want.Name, fmt.Errorf("its parent service was not created"))
			continue
		}
		params := map[string]any{"serviceid": u.ID, "name": u.Want.Name, "algorithm": 2, "parents": par, "problem_tags": tagList(u.Want.ProblemTags, true)}
		if err := c.Call(ctx, "service.update", params, nil); err != nil {
			fail(u.Want.Name, err)
			continue
		}
		a.Updated++
	}
	return a, nil
}
