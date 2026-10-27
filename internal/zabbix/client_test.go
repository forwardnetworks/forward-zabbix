package zabbix

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

// A fake API that records what it was sent, to check the wire contract: JSON-RPC 2.0, a bearer token on every
// call but login, and an API error surfaced as *Error.
func TestAPICallSendsBearerTokenAndSurfacesErrors(t *testing.T) {
	var gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMethod = req.Method
		if req.Method == "host.get" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":[{"hostid":"1"}],"id":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params.","data":"nope"},"id":1}`))
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err := c.Call(context.Background(), "host.get", map[string]any{}, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if gotAuth != "Bearer tok" || gotMethod != "host.get" {
		t.Fatalf("auth %q method %q", gotAuth, gotMethod)
	}
	err = c.Call(context.Background(), "item.get", map[string]any{}, nil)
	if e, ok := err.(*Error); !ok || e.Code != -32602 || e.Data != "nope" {
		t.Fatalf("err = %#v", err)
	}
}

// A fake trapper that checks the framing (ZBXD\x01, little-endian length, JSON) and answers like Zabbix does.
func TestPushValuesUsesTrapperFramingAndParsesCounts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan map[string]any, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		head := make([]byte, 13)
		_, _ = io.ReadFull(conn, head)
		body := make([]byte, binary.LittleEndian.Uint64(head[5:]))
		_, _ = io.ReadFull(conn, body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if string(head[:5]) != "ZBXD\x01" {
			m = map[string]any{"bad": string(head[:5])}
		}
		got <- m
		resp := []byte(`{"response":"success","info":"processed: 2; failed: 1; total: 3; seconds spent: 0.0001"}`)
		out := append([]byte("ZBXD\x01"), make([]byte, 8)...)
		binary.LittleEndian.PutUint64(out[5:], uint64(len(resp)))
		_, _ = conn.Write(append(out, resp...))
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	c := &Client{cfg: config.Zabbix{Server: "127.0.0.1", TrapperPort: port}}
	processed, failed, err := c.PushValues(context.Background(), []Value{{"h1", "k1", "v1"}, {"h2", "k2", "v2"}, {"h3", "k3", "v3"}})
	if err != nil || processed != 2 || failed != 1 {
		t.Fatalf("processed %d failed %d err %v", processed, failed, err)
	}
	m := <-got
	data, _ := m["data"].([]any)
	if m["request"] != "sender data" || len(data) != 3 {
		t.Fatalf("request = %v", m)
	}
	if first := data[0].(map[string]any); first["host"] != "h1" || first["key"] != "k1" || first["value"] != "v1" {
		t.Fatalf("first item = %v", first)
	}
}

func TestKeyKindAndParam(t *testing.T) {
	for key, want := range map[string][2]string{
		"fwd.if.in.octets[Ethernet1]":  {"in.octets", "Ethernet1"},
		"fwd.mem.used[RAM (Cache)]":    {"used", "RAM (Cache)"},
		`fwd.mem.size["RAM, buffers"]`: {"size", "RAM, buffers"},
		"fwd.cpu.core[7]":              {"core", "7"},
	} {
		if k, p := keyKindAndParam(key); k != want[0] || p != want[1] {
			t.Errorf("%s -> %q %q, want %v", key, k, p, want)
		}
	}
}

// ReachabilityTriggers must ask for this tool's own triggers only (tag, no templates) and read dependencies.
func TestReachabilityTriggersRequestAndParsing(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Params
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":[
			{"triggerid":"10","hosts":[{"hostid":"1"}],"dependencies":[{"triggerid":"20"},{"triggerid":"21"}]},
			{"triggerid":"11","hosts":[{"hostid":"2"}],"dependencies":[]},
			{"triggerid":"12","hosts":[],"dependencies":[]}],"id":1}`))
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	trigs, err := c.ReachabilityTriggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trigs) != 2 || trigs[0].HostID != "1" || len(trigs[0].Deps) != 2 || trigs[1].TriggerID != "11" {
		t.Fatalf("parsed %+v; a trigger without exactly one host must be skipped", trigs)
	}
	if got["templated"] != false || got["monitored"] != nil {
		t.Errorf("must exclude templates and include disabled hosts, params %v", got)
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["value"] != "reachability" {
		t.Errorf("must filter on the reachability tag, params %v", got)
	}
}

// Removals first, then additions; a host whose removal failed gets no addition.
func TestApplyTopologyOrderAndFailures(t *testing.T) {
	var calls []string
	failFor := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				TriggerID    string              `json:"triggerid"`
				Dependencies []map[string]string `json:"dependencies"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params.TriggerID == failFor {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32500,"message":"refused","data":"loop"},"id":1}`))
			return
		}
		deps := ""
		for _, d := range req.Params.Dependencies {
			deps += d["triggerid"] + ","
		}
		calls = append(calls, req.Params.TriggerID+"<-"+deps)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"triggerids":["1"]},"id":1}`))
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	p := plan.TopologyPlan{Change: []plan.DepChange{
		{Host: "x", TriggerID: "1", Was: []string{"p"}, Parent: "q", Keep: []string{"k"}, Want: []string{"k", "tq"}}, // moved
		{Host: "y", TriggerID: "2", Parent: "q", Want: []string{"tq"}},                                               // new
		{Host: "z", TriggerID: "3", Was: []string{"p"}, Keep: []string{"k"}, Want: []string{"k"}},                    // dropped
	}}
	a, err := c.ApplyTopology(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1<-k,", "3<-k,", "1<-k,tq,", "2<-tq,"}
	if !reflect.DeepEqual(calls, want) || a.Updated != 3 || len(a.Failures) != 0 {
		t.Errorf("calls %v updated %d failures %v, want %v / 3 / none", calls, a.Updated, a.Failures, want)
	}

	calls, failFor = nil, "1"
	a, _ = c.ApplyTopology(context.Background(), p)
	want = []string{"3<-k,", "2<-tq,"}
	if !reflect.DeepEqual(calls, want) || a.Updated != 2 || len(a.Failures) != 1 || !strings.HasPrefix(a.Failures[0], "x:") {
		t.Errorf("after a refused removal: calls %v updated %d failures %v, want %v / 2 / one for x", calls, a.Updated, a.Failures, want)
	}
}

// A problem that opened and recovered inside the window is evidence of the change: it must come from events, not from
// the problem list, which only holds what is still open.
func TestProblemsBetweenReadsEventsAndKeepsRecoveredOnes(t *testing.T) {
	var methods []string
	var eventParams, triggerParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		methods = append(methods, req.Method)
		if req.Method == "trigger.get" {
			triggerParams = req.Params
		}
		switch req.Method {
		case "event.get":
			eventParams = req.Params
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":[
				{"eventid":"1","name":"down","clock":"1700000000","severity":"4","objectid":"10","r_eventid":"2","suppressed":"0"},
				{"eventid":"3","name":"still down","clock":"1700000100","severity":"5","objectid":"11","r_eventid":"0","suppressed":"1"}],"id":1}`))
		case "trigger.get":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":[
				{"triggerid":"10","hosts":[{"host":"core-1"}]},{"triggerid":"11","hosts":[{"host":"edge-1"}]}],"id":1}`))
		}
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ProblemsBetween(context.Background(), time.Unix(1699999000, 0), time.Unix(1700001000, 0), []string{"7", "8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Host != "core-1" || !got[0].Resolved || got[0].Suppressed || got[1].Host != "edge-1" || got[1].Resolved || !got[1].Suppressed {
		t.Fatalf("got %+v", got)
	}
	if methods[0] != "event.get" || eventParams["value"] != float64(1) || eventParams["source"] != float64(0) || eventParams["object"] != float64(0) ||
		eventParams["time_from"] != float64(1699999000) || eventParams["time_till"] != float64(1700001000) {
		t.Errorf("must read trigger PROBLEM events in the window, called %v with %v", methods, eventParams)
	}
	if ids, _ := eventParams["hostids"].([]any); len(ids) != 2 {
		t.Errorf("must ask only for the given hosts, params %v", eventParams)
	}
	if _, filtered := triggerParams["monitored"]; filtered {
		t.Errorf("a host or trigger disabled after the change must still resolve to its host, params %v", triggerParams)
	}
}

func TestProblemsBetweenWithNoHostsAsksNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	c, _ := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	got, err := c.ProblemsBetween(context.Background(), time.Unix(1, 0), time.Unix(2, 0), nil)
	if err != nil || got != nil || called {
		t.Errorf("no hosts means no query: got %v err %v called %v", got, err, called)
	}
}

func TestMaintenanceReadsItsPeriodAndExpandsHostGroups(t *testing.T) {
	respond := func(result string) []byte { return []byte(`{"jsonrpc":"2.0","result":` + result + `,"id":1}`) }
	var calls []string
	var groupIDs any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls = append(calls, req.Method)
		switch req.Method {
		case "maintenance.get":
			if req.Params["filter"].(map[string]any)["name"] == "twice" {
				_, _ = w.Write(respond(`[{"name":"twice"},{"name":"twice"}]`))
				return
			}
			_, _ = w.Write(respond(`[{"name":"win","active_since":"1700000000","active_till":"1700003600",
				"hosts":[{"host":"direct-1"}],"hostgroups":[{"groupid":"5"}]}]`))
		case "host.get":
			groupIDs = req.Params["groupids"]
			_, _ = w.Write(respond(`[{"host":"grouped-1"},{"host":"direct-1"}]`))
		}
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.Maintenance(context.Background(), "win")
	if err != nil {
		t.Fatal(err)
	}
	if m.Since.Unix() != 1700000000 || m.Till.Unix() != 1700003600 {
		t.Errorf("period = %v to %v", m.Since, m.Till)
	}
	if !reflect.DeepEqual(m.Hosts, []string{"direct-1", "grouped-1"}) {
		t.Errorf("hosts = %v, want the direct host and the group's, once each, sorted", m.Hosts)
	}
	if g, _ := groupIDs.([]any); len(g) != 1 || g[0] != "5" {
		t.Errorf("must expand the maintenance's host groups, got %v", groupIDs)
	}
	if _, err := c.Maintenance(context.Background(), "twice"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("two maintenances with one name must be refused, got %v", err)
	}
}

// Services are created parent first; one whose parent failed is skipped and reported; an update keeps parents this
// tool does not own; the root is sent an empty parent list, not null.
func TestApplyServicesCreatesParentsFirstAndSkipsOrphans(t *testing.T) {
	type call struct {
		Method string
		Params map[string]any
	}
	var calls []call
	failName := ""
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params["name"] == failName {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32500,"message":"refused","data":"x"},"id":1}`))
			return
		}
		calls = append(calls, call{req.Method, req.Params})
		n++
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"serviceids":["` + strings.Repeat("9", n) + `"]},"id":1}`))
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), config.Zabbix{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	p := plan.ServicesPlan{
		IDs: map[string]string{"device|old": "50"},
		Create: []plan.SvcWant{
			{Key: "network|", Name: "root", Tags: []plan.Tag{{Tag: "managed-by", Value: "forward-zabbix"}}},
			{Key: "device|a", Name: "a", ParentKey: "network|"},
			{Key: "self|a", Name: "a self", ParentKey: "device|a", ProblemTags: []plan.Tag{{Tag: "fwd-device", Value: "a"}}},
		},
		Update: []plan.SvcUpdate{{ID: "50", Want: plan.SvcWant{Key: "device|old", Name: "old", ParentKey: "device|a"}, KeepParents: []string{"77"}}},
	}
	a, err := c.ApplyServices(context.Background(), p)
	if err != nil || a.Created != 3 || a.Updated != 1 || len(a.Failures) != 0 {
		t.Fatalf("applied %+v err %v", a, err)
	}
	if calls[0].Method != "service.create" || calls[0].Params["parents"] == nil {
		t.Errorf("the root must be sent an empty parent list, not null: %v", calls[0].Params)
	}
	if par, _ := calls[1].Params["parents"].([]any); len(par) != 1 || par[0].(map[string]any)["serviceid"] != "9" {
		t.Errorf("a must be created under the root's new id, got %v", calls[1].Params["parents"])
	}
	if _, has := calls[1].Params["problem_tags"]; has {
		t.Error("a container must not be sent problem tags")
	}
	pt, has := calls[2].Params["problem_tags"].([]any)
	if !has || len(pt) != 1 || pt[0].(map[string]any)["operator"] != float64(0) {
		t.Errorf("the leaf must be sent its problem tags with an operator, got %v", calls[2].Params["problem_tags"])
	}
	for _, i := range []int{0, 1, 2} {
		if tags, _ := calls[i].Params["tags"].([]any); len(tags) > 0 {
			if _, bad := tags[0].(map[string]any)["operator"]; bad {
				t.Errorf("Zabbix refuses an operator on ordinary service tags, call %d sent one: %v", i, tags)
			}
		}
	}
	if pt, ok := calls[3].Params["problem_tags"].([]any); !ok || len(pt) != 0 || calls[3].Params["algorithm"] != float64(2) {
		t.Errorf("an update must send empty problem tags for a container and restore the status rule, got %v", calls[3].Params)
	}
	if par, _ := calls[3].Params["parents"].([]any); calls[3].Method != "service.update" || len(par) != 2 || par[1].(map[string]any)["serviceid"] != "77" {
		t.Errorf("an update must carry the new parent and keep the person's, got %v", calls[3].Params)
	}

	calls, failName, n = nil, "a", 0
	a, _ = c.ApplyServices(context.Background(), p)
	if a.Created != 1 || len(a.Failures) < 3 || !strings.Contains(strings.Join(a.Failures, "|"), "a self: its parent service was not created") {
		t.Errorf("a failed container must take its leaf down with it and say so: %+v", a)
	}
}
