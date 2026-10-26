// Package receiver receives Forward webhooks. It acknowledges fast and works off a queue, because Forward does
// not retry and gives a handler 10 seconds.
package receiver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

const (
	MaxBody       = 64 * 1024
	SnapshotReady = "SNAPSHOT_READY"
	seenLimit     = 4096 // de-duplication memory; the oldest keys are forgotten first
	queueSize     = 1024
)

var checkFailures = map[string]bool{"INTENT_VERIFICATION_FAILURE": true, "NQE_VERIFICATION_FAILURE": true}

// Event is a Forward webhook event.
type Event struct {
	Type       string
	NetworkID  string
	SnapshotID string
	Raw        map[string]any
}

// ParseEvent returns an error for anything that is not a Forward event we understand.
func ParseEvent(body []byte) (Event, error) {
	var data map[string]any
	if err := decodeObject(body, &data); err != nil {
		return Event{}, fmt.Errorf("not a JSON object: %v", err)
	}
	var missing []string
	for _, k := range []string{"type", "networkId", "snapshotId"} {
		if v, ok := data[k]; !ok || v == nil || v == "" || v == false || v == json.Number("0") {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Event{}, fmt.Errorf("missing field(s): %s", strings.Join(missing, ", "))
	}
	return Event{fmt.Sprint(data["type"]), fmt.Sprint(data["networkId"]), fmt.Sprint(data["snapshotId"]), data}, nil
}

// decodeObject parses a JSON object keeping numbers as their original text, so a numeric id is not turned into
// 1.234567e+06.
func decodeObject(body []byte, out *map[string]any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	return d.Decode(out)
}

// Authorized checks an HTTP basic-auth header in constant time.
func Authorized(header, user, password string) bool {
	if !strings.HasPrefix(header, "Basic ") {
		return false
	}
	given, err := base64.StdEncoding.Strict().DecodeString(header[6:])
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(given, []byte(user+":"+password)) == 1
}

func raw(e Event, key string) string {
	switch v := e.Raw[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// FailureValues turns a check-failure event into one trapper value on the network host.
func FailureValues(e Event) []plan.Value {
	name := raw(e, "checkName")
	if name == "" {
		name = raw(e, "checkPath")
	}
	if name == "" {
		name = "check"
	}
	kind := raw(e, "failureType")
	if kind == "" {
		kind = raw(e, "checkStatus")
	}
	if kind == "" {
		kind = "failing"
	}
	detail := name + ": " + kind
	if n, ok := e.Raw["numViolations"]; ok && n != nil {
		detail += fmt.Sprintf(" (%v violations)", n)
	}
	return []plan.Value{{Host: plan.NetworkHostName(e.NetworkID), Key: "fwd.check.failure",
		Value: fmt.Sprintf("%s [snapshot %s]", detail, e.SnapshotID)}}
}

// Handlers are what the receiver calls, off the request path.
type Handlers struct {
	OnSnapshot func(ctx context.Context, networkID string) error
	OnValues   func(ctx context.Context, networkID string, values []plan.Value) error
}

// Receiver is an http.Handler for Forward's webhook.
type Receiver struct {
	user, password string
	networks       map[string]bool
	path           string
	h              Handlers
	jobs           chan func(context.Context) error
	mu             sync.Mutex
	seen           map[string]bool
	order          []string
	logf           func(string, ...any)
}

// New validates its configuration; it will not start without credentials or at least one network.
func New(user, password string, networks []string, path string, h Handlers) (*Receiver, error) {
	if user == "" || password == "" {
		return nil, errors.New("a webhook user and password are required")
	}
	set := map[string]bool{}
	for _, n := range networks {
		if n != "" {
			set[n] = true
		}
	}
	if len(set) == 0 {
		return nil, errors.New("at least one network id is required")
	}
	return &Receiver{user: user, password: password, networks: set, path: path, h: h,
		jobs: make(chan func(context.Context) error, queueSize), seen: map[string]bool{},
		logf: func(f string, a ...any) { log.Printf("fwdzbx.receiver "+f, a...) }}, nil
}

// Pending is how many accepted events are waiting for the worker.
func (r *Receiver) Pending() int { return len(r.jobs) }

func (r *Receiver) remember(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[key] {
		return false
	}
	r.seen[key] = true
	r.order = append(r.order, key)
	if len(r.order) > seenLimit {
		delete(r.seen, r.order[0])
		r.order = r.order[1:]
	}
	return true
}

// accept decides what to do with a verified event and returns a short outcome for the log and the response.
func (r *Receiver) accept(e Event) (string, bool) {
	if !r.networks[e.NetworkID] {
		return "ignored: network not configured", false
	}
	switch {
	case e.Type == SnapshotReady, checkFailures[e.Type]:
	default:
		return "ignored: event type " + e.Type, false
	}
	if !r.remember(e.Type + "|" + e.SnapshotID + ":" + raw(e, "checkPath")) {
		return "ignored: duplicate", false
	}
	var job func(context.Context) error
	if e.Type == SnapshotReady {
		job = func(ctx context.Context) error { return r.h.OnSnapshot(ctx, e.NetworkID) }
	} else {
		job = func(ctx context.Context) error { return r.h.OnValues(ctx, e.NetworkID, FailureValues(e)) }
	}
	select {
	case r.jobs <- job:
		return "queued", true
	default:
		return "busy: queue full", false
	}
}

// Work runs queued events one at a time until ctx ends. One bad event does not stop it.
func (r *Receiver) Work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-r.jobs:
			jctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			if err := job(jctx); err != nil {
				r.logf("event handling failed: %v", err)
			}
			cancel()
		}
	}
}

func reply(w http.ResponseWriter, code int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, text)
}

// ServeHTTP never logs headers or bodies.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.logf("%s %s", req.Method, req.URL.Path)
	if req.Method != http.MethodPost || req.URL.Path != r.path {
		reply(w, http.StatusNotFound, "not found")
		return
	}
	if !Authorized(req.Header.Get("Authorization"), r.user, r.password) {
		reply(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if req.ContentLength <= 0 {
		reply(w, http.StatusBadRequest, "bad body size")
		return
	}
	if req.ContentLength > MaxBody {
		reply(w, http.StatusRequestEntityTooLarge, "bad body size")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxBody))
	if err != nil {
		reply(w, http.StatusBadRequest, "bad body")
		return
	}
	e, err := ParseEvent(body)
	if err != nil {
		reply(w, http.StatusBadRequest, err.Error())
		return
	}
	outcome, queued := r.accept(e)
	r.logf("%s network=%s snapshot=%s -> %s", e.Type, e.NetworkID, e.SnapshotID, outcome)
	switch {
	case queued:
		reply(w, http.StatusAccepted, outcome)
	case strings.HasPrefix(outcome, "busy"):
		reply(w, http.StatusServiceUnavailable, outcome)
	default:
		reply(w, http.StatusOK, outcome)
	}
}

// Serve listens on addr and returns when ctx ends or the listener fails. Slow clients are cut off.
func (r *Receiver) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go r.Work(ctx)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
