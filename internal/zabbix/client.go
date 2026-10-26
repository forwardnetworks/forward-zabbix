// Package zabbix is a small Zabbix 7.0 client: JSON-RPC over HTTP for the API and the trapper protocol for values.
// It covers only the calls this project makes.
package zabbix

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
)

// Client talks to one Zabbix server. Close ends a session login; a token needs no logout.
type Client struct {
	cfg      config.Zabbix
	endpoint string
	http     *http.Client
	token    string
	session  bool
	nextID   int
}

// Error is an error object returned by the Zabbix API.
type Error struct {
	Method  string
	Code    int
	Message string
	Data    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("zabbix %s: %s (%s) [%d]", e.Method, e.Message, e.Data, e.Code)
}

// Connect logs in (with the API token, or user and password) and returns a client.
func Connect(ctx context.Context, cfg config.Zabbix) (*Client, error) {
	endpoint := strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(endpoint, "api_jsonrpc.php") {
		endpoint += "/api_jsonrpc.php"
	}
	c := &Client{cfg: cfg, endpoint: endpoint, http: &http.Client{Timeout: 60 * time.Second}}
	if cfg.Token != "" {
		c.token = cfg.Token
		return c, nil
	}
	var tok string
	if err := c.call(ctx, "user.login", map[string]any{"username": cfg.User, "password": cfg.Password}, &tok); err != nil {
		return nil, err
	}
	c.token, c.session = tok, true
	return c, nil
}

// Close ends a user/password session. It is a no-op for a token.
func (c *Client) Close(ctx context.Context) {
	if c.session {
		_ = c.call(ctx, "user.logout", []string{}, nil)
		c.session = false
	}
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

// Call invokes any API method and decodes its result into out (nil to discard it).
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	return c.call(ctx, method, params, out)
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.nextID++
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": c.nextID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	if c.token != "" && method != "user.login" && method != "apiinfo.version" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("zabbix %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return fmt.Errorf("zabbix %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zabbix %s: HTTP %d", method, resp.StatusCode)
	}
	var r rpcResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("zabbix %s: bad response: %w", method, err)
	}
	if r.Error != nil {
		return &Error{method, r.Error.Code, r.Error.Message, r.Error.Data}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// Value is one trapper value.
type Value struct{ Host, Key, Value string }

var infoRE = regexp.MustCompile(`processed:\s*(\d+);\s*failed:\s*(\d+)`)

// PushValues sends trapper values and returns (processed, failed). The protocol only returns counts.
func (c *Client) PushValues(ctx context.Context, values []Value) (processed, failed int, err error) {
	type item struct {
		Host  string `json:"host"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	data := make([]item, len(values))
	for i, v := range values {
		data[i] = item{v.Host, v.Key, v.Value}
	}
	payload, err := json.Marshal(map[string]any{"request": "sender data", "data": data})
	if err != nil {
		return 0, 0, err
	}
	frame := append([]byte("ZBXD\x01"), make([]byte, 8)...)
	binary.LittleEndian.PutUint64(frame[5:], uint64(len(payload)))
	frame = append(frame, payload...)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.cfg.Server, strconv.Itoa(c.cfg.TrapperPort)))
	if err != nil {
		return 0, 0, fmt.Errorf("zabbix trapper: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write(frame); err != nil {
		return 0, 0, fmt.Errorf("zabbix trapper: %w", err)
	}
	head := make([]byte, 13)
	if _, err := io.ReadFull(conn, head); err != nil || string(head[:5]) != "ZBXD\x01" {
		return 0, 0, fmt.Errorf("zabbix trapper: bad response header")
	}
	n := binary.LittleEndian.Uint64(head[5:])
	if n > 1<<20 {
		return 0, 0, fmt.Errorf("zabbix trapper: response too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return 0, 0, fmt.Errorf("zabbix trapper: %w", err)
	}
	var resp struct {
		Response string `json:"response"`
		Info     string `json:"info"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, 0, fmt.Errorf("zabbix trapper: bad response: %w", err)
	}
	m := infoRE.FindStringSubmatch(resp.Info)
	if resp.Response != "success" || m == nil {
		return 0, 0, fmt.Errorf("zabbix trapper: %s %s", resp.Response, resp.Info)
	}
	processed, _ = strconv.Atoi(m[1])
	failed, _ = strconv.Atoi(m[2])
	return processed, failed, nil
}
