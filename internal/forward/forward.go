// Package forward reads what this project needs from Forward through the Forward Go SDK: devices, links and the
// newest snapshot. It never writes to Forward.
package forward

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/forwardnetworks/forward-go-sdk"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

// Field names verified against a live Forward (an unknown field is a runtime error).
const devicesQuery = `
foreach d in network.devices
select {
  name: d.name,
  site: d.locationName,
  tags: d.tagNames,
  vendor: d.platform.vendor,
  model: d.platform.model,
  os: d.platform.os,
  osVersion: d.platform.osVersion,
  deviceType: d.platform.deviceType,
  ips: d.platform.managementIps,
  collected: d.snapshotInfo.result
}
`

const linksQuery = `
foreach d in network.devices
foreach i in d.interfaces
foreach l in i.links
select {device: d.name, iface: i.name, peer: l.deviceName, peerIface: l.ifaceName}
`

const pageSize = 5000

// NewClient builds an SDK client from the FORWARD_* environment settings.
func NewClient(cfg config.Forward) (*sdk.Client, error) {
	c := sdk.Config{BaseURL: cfg.URL, Username: cfg.Username, Password: cfg.Password,
		UserAgent: "forward-zabbix", InsecureSkipVerify: cfg.InsecureSkipVerify}
	if cfg.CAFile != "" {
		pool, err := sdk.LoadRootCAs(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		c.RootCAs = pool
	}
	if cfg.CAFile == "" && !cfg.InsecureSkipVerify {
		// the SDK's own 60 s cap is short for a write that Forward answers only after storing it; a custom TLS
		// setup keeps the SDK's client, whose transport carries those settings
		c.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	return sdk.NewClient(c)
}

// query runs an NQE query against the newest processed snapshot and returns every row.
func query(ctx context.Context, c *sdk.Client, networkID, q string) ([]sdk.NQERecord, error) {
	var all []sdk.NQERecord
	for offset := int32(0); ; {
		off, limit := offset, int32(pageSize)
		res, _, err := c.NQE.Run(ctx, networkID, "", sdk.NQEQueryRequest{Query: q, Options: &sdk.NQEOptions{Offset: &off, Limit: &limit}})
		if err != nil {
			return nil, err
		}
		all = append(all, res.Items...)
		if len(res.Items) == 0 || int64(len(all)) >= res.TotalNumItems {
			return all, nil
		}
		offset = int32(len(all))
	}
}

func str(r sdk.NQERecord, key string) string {
	raw, ok := r[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func strs(r sdk.NQERecord, key string) []string {
	var out []string
	if raw, ok := r[key]; ok {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// collected normalises snapshotInfo.result, a union: a plain string, or {case, value} for a failure.
func collected(r sdk.NQERecord) string {
	raw, ok := r["collected"]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var u struct {
		Case  string          `json:"case"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return ""
	}
	val := strings.Trim(string(u.Value), " ")
	var vs string
	if json.Unmarshal(u.Value, &vs) == nil {
		val = vs
	} else if val == "null" {
		val = ""
	}
	return strings.Trim(u.Case+":"+val, ":")
}

// Devices returns the network's devices sorted by name.
func Devices(ctx context.Context, c *sdk.Client, networkID string) ([]plan.Device, error) {
	rows, err := query(ctx, c, networkID, devicesQuery)
	if err != nil {
		return nil, err
	}
	devices := make([]plan.Device, 0, len(rows))
	for _, r := range rows {
		tags := strs(r, "tags")
		sort.Strings(tags)
		devices = append(devices, plan.Device{Name: str(r, "name"), Site: str(r, "site"), Vendor: str(r, "vendor"),
			Model: str(r, "model"), OS: str(r, "os"), OSVersion: str(r, "osVersion"), DeviceType: str(r, "deviceType"),
			IPs: strs(r, "ips"), Tags: tags, Collected: collected(r)})
	}
	sort.SliceStable(devices, func(i, j int) bool { return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name) })
	return devices, nil
}

// Links returns each device's direct links, keyed by device name.
func Links(ctx context.Context, c *sdk.Client, networkID string) (map[string][]plan.Link, error) {
	rows, err := query(ctx, c, networkID, linksQuery)
	if err != nil {
		return nil, err
	}
	out := map[string][]plan.Link{}
	for _, r := range rows {
		d := str(r, "device")
		out[d] = append(out[d], plan.Link{Iface: str(r, "iface"), Peer: str(r, "peer"), PeerIface: str(r, "peerIface")})
	}
	return out, nil
}

// Snapshot is the newest usable snapshot.
type Snapshot struct {
	ID    string
	Epoch int64
}

// NewestSnapshot is the newest non-draft, non-predicted snapshot that has data. It is chosen from the listing by
// creation time, not by the SDK's LatestProcessed helper, which has returned an older snapshot than a newer
// processed one.
func NewestSnapshot(ctx context.Context, c *sdk.Client, networkID string) (Snapshot, error) {
	list, _, err := c.Snapshots.List(ctx, networkID, sdk.SnapshotListOptions{})
	if err != nil {
		return Snapshot{}, err
	}
	return pickNewest(networkID, list)
}

func pickNewest(networkID string, list []sdk.Snapshot) (Snapshot, error) {
	var best Snapshot
	var bestTime time.Time
	for _, s := range list {
		if s.IsDraft || s.IsPredicted || s.State != "PROCESSED" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
		if err != nil {
			continue
		}
		if best.ID == "" || t.After(bestTime) {
			best, bestTime = Snapshot{ID: s.ID.String(), Epoch: t.Unix()}, t
		}
	}
	if best.ID == "" {
		return Snapshot{}, fmt.Errorf("network %s has no processed snapshot", networkID)
	}
	return best, nil
}
