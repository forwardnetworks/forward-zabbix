package zabbix

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
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
