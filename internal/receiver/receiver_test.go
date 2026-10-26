// Receiver behaviour from the requirements: authenticate, validate, scope to configured networks, ack fast.
package receiver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

type calls struct {
	mu        sync.Mutex
	snapshots []string
	values    [][]plan.Value
	done      chan struct{}
}

func (c *calls) signal() {
	select {
	case c.done <- struct{}{}:
	default:
	}
}

func setup(t *testing.T) (*Receiver, *httptest.Server, *calls) {
	t.Helper()
	c := &calls{done: make(chan struct{}, 8)}
	r, err := New("hook", "s3cret", []string{"7"}, "/hook", Handlers{
		OnSnapshot: func(_ context.Context, n string) error {
			c.mu.Lock()
			c.snapshots = append(c.snapshots, n)
			c.mu.Unlock()
			c.signal()
			return nil
		},
		OnValues: func(_ context.Context, n string, v []plan.Value) error {
			c.mu.Lock()
			c.values = append(c.values, append([]plan.Value{{Host: n}}, v...))
			c.mu.Unlock()
			c.signal()
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	r.logf = func(string, ...any) {}
	ctx, cancel := context.WithCancel(context.Background())
	go r.Work(ctx)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); cancel() })
	return r, srv, c
}

var auth = "Basic " + base64.StdEncoding.EncodeToString([]byte("hook:s3cret"))

func post(t *testing.T, srv *httptest.Server, body any, authHeader, path string) (int, string) {
	t.Helper()
	var b []byte
	switch v := body.(type) {
	case []byte:
		b = v
	default:
		b, _ = json.Marshal(v)
	}
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(string(b)))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text)
}

func event(extra map[string]any) map[string]any {
	e := map[string]any{"type": "SNAPSHOT_READY", "networkId": "7", "snapshotId": "100"}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func wait(t *testing.T, c *calls) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler was not called")
	}
}

func TestRejectsMissingAndWrongCredentials(t *testing.T) {
	r, srv, c := setup(t)
	for name, h := range map[string]string{"none": "", "wrong": "Basic " + base64.StdEncoding.EncodeToString([]byte("hook:wrong")), "garbage": "Basic !!!not-base64"} {
		if code, _ := post(t, srv, event(nil), h, "/hook"); code != 401 {
			t.Errorf("%s: got %d, want 401", name, code)
		}
	}
	if len(c.snapshots) != 0 || r.Pending() != 0 {
		t.Fatal("work was queued for an unauthenticated request")
	}
}

func TestRejectsBadInputWithoutQueuingWork(t *testing.T) {
	r, srv, _ := setup(t)
	cases := []struct {
		name string
		body any
		path string
		want int
	}{
		{"not json", []byte("not json"), "/hook", 400},
		{"missing fields", map[string]any{"type": "SNAPSHOT_READY"}, "/hook", 400},
		{"wrong path", event(nil), "/elsewhere", 404},
		{"too large", []byte(strings.Repeat("x", MaxBody+1)), "/hook", 413},
	}
	for _, c := range cases {
		if code, _ := post(t, srv, c.body, auth, c.path); code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, code, c.want)
		}
	}
	if r.Pending() != 0 {
		t.Fatal("bad input queued work")
	}
}

func TestSnapshotReadyForAConfiguredNetworkIsAcknowledgedAndHandled(t *testing.T) {
	_, srv, c := setup(t)
	if code, text := post(t, srv, event(nil), auth, "/hook"); code != 202 || text != "queued" {
		t.Fatalf("got %d %q", code, text)
	}
	wait(t, c)
	if len(c.snapshots) != 1 || c.snapshots[0] != "7" {
		t.Fatalf("snapshots = %v", c.snapshots)
	}
}

func TestEventsForOtherNetworksAreIgnored(t *testing.T) {
	r, srv, _ := setup(t)
	code, text := post(t, srv, event(map[string]any{"networkId": "999"}), auth, "/hook")
	if code != 200 || !strings.Contains(text, "not configured") || r.Pending() != 0 {
		t.Fatalf("got %d %q pending %d", code, text, r.Pending())
	}
}

func TestTheSameSnapshotEventIsHandledOnce(t *testing.T) {
	_, srv, c := setup(t)
	if _, text := post(t, srv, event(nil), auth, "/hook"); text != "queued" {
		t.Fatal(text)
	}
	wait(t, c)
	if _, text := post(t, srv, event(nil), auth, "/hook"); !strings.Contains(text, "duplicate") {
		t.Fatalf("second delivery: %q", text)
	}
}

func TestCheckFailureBecomesAValueOnTheNetworkHost(t *testing.T) {
	_, srv, c := setup(t)
	payload := map[string]any{"type": "INTENT_VERIFICATION_FAILURE", "networkId": "7", "snapshotId": "100",
		"checkName": "no-telnet", "checkPath": "/security", "failureType": "VIOLATED"}
	if code, _ := post(t, srv, payload, auth, "/hook"); code != 202 {
		t.Fatalf("code %d", code)
	}
	wait(t, c)
	got := c.values[0]
	want := plan.Value{Host: "forward-network-7", Key: "fwd.check.failure", Value: "no-telnet: VIOLATED [snapshot 100]"}
	if got[0].Host != "7" || len(got) != 2 || got[1] != want {
		t.Fatalf("values = %+v", got)
	}
}

func TestUnknownEventTypesAreAcknowledgedButNotActedOn(t *testing.T) {
	r, srv, _ := setup(t)
	code, text := post(t, srv, event(map[string]any{"type": "SOMETHING_NEW"}), auth, "/hook")
	if code != 200 || !strings.Contains(text, "event type") || r.Pending() != 0 {
		t.Fatalf("got %d %q", code, text)
	}
}

func TestWillNotStartWithoutCredentialsOrNetworks(t *testing.T) {
	h := Handlers{}
	if _, err := New("", "x", []string{"1"}, "/h", h); err == nil {
		t.Error("accepted an empty user")
	}
	if _, err := New("u", "", []string{"1"}, "/h", h); err == nil {
		t.Error("accepted an empty password")
	}
	if _, err := New("u", "p", nil, "/h", h); err == nil {
		t.Error("accepted no networks")
	}
}

func TestFailureValueIncludesViolationCountWhenPresent(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"type":"NQE_VERIFICATION_FAILURE","networkId":"7","snapshotId":"5","checkName":"ntp","numViolations":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := FailureValues(ev)[0].Value; got != "ntp: failing (3 violations) [snapshot 5]" {
		t.Fatalf("got %q", got)
	}
}

func TestOtherMethodsGet404Not501(t *testing.T) {
	_, srv, _ := setup(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
}

func TestTheDedupMemoryIsBounded(t *testing.T) {
	r, _, _ := setup(t)
	for i := 0; i < seenLimit+50; i++ {
		r.remember(string(rune('a'+i%26)) + "|" + strings.Repeat("x", i%7) + "#" + json.Number(strings.Repeat("1", 1+i%5)).String() + string(rune(i)))
	}
	if len(r.seen) > seenLimit {
		t.Fatalf("seen grew to %d", len(r.seen))
	}
}
