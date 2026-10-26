// Package fwdzbx is the public API of the Forward to Zabbix integration. The command line is a thin layer over
// these functions; pipelines should call them directly.
//
// Every function reads Forward and Zabbix settings from the environment unless you pass them in with Options.
// Functions that can change something take Apply and do nothing but read when it is false.
package fwdzbx

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/forwardnetworks/forward-go-sdk"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/forward"
	"github.com/forwardnetworks/forward-zabbix/internal/perf"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
	"github.com/forwardnetworks/forward-zabbix/internal/zabbix"
)

// Types returned by the API, re-exported so callers can name them.
type (
	Device     = plan.Device
	Link       = plan.Link
	SyncPlan   = plan.SyncPlan
	Gap        = plan.Gap
	Value      = plan.Value
	Applied    = zabbix.Applied
	ZabbixCfg  = config.Zabbix
	ForwardCfg = config.Forward
)

// ErrConfig is wrapped by every missing or invalid setting.
var ErrConfig = config.ErrConfig

// Options overrides the environment. The zero value reads everything from the environment.
type Options struct {
	Zabbix  *config.Zabbix
	Forward *config.Forward
	// Client, if set, is used for Forward instead of building one; the caller closes nothing (the SDK has no Close).
	Client *sdk.Client
}

func (o Options) zabbixCfg() (config.Zabbix, error) {
	if o.Zabbix != nil {
		return *o.Zabbix, nil
	}
	return config.ZabbixFromEnv()
}

func (o Options) forwardClient() (*sdk.Client, error) {
	if o.Client != nil {
		return o.Client, nil
	}
	cfg := config.Forward{}
	if o.Forward != nil {
		cfg = *o.Forward
	} else {
		var err error
		if cfg, err = config.ForwardFromEnv(); err != nil {
			return nil, err
		}
	}
	return forward.NewClient(cfg)
}

func (o Options) connect(ctx context.Context) (*zabbix.Client, error) {
	cfg, err := o.zabbixCfg()
	if err != nil {
		return nil, err
	}
	return zabbix.Connect(ctx, cfg)
}

// Devices lists the devices Forward holds for the network. Read-only.
func Devices(ctx context.Context, networkID string, o Options) ([]Device, error) {
	c, err := o.forwardClient()
	if err != nil {
		return nil, err
	}
	return forward.Devices(ctx, c, networkID)
}

// CoverageGap diffs Forward's inventory against every Zabbix host. Read-only.
func CoverageGap(ctx context.Context, networkID string, o Options) (Gap, error) {
	devices, err := Devices(ctx, networkID, o)
	if err != nil {
		return Gap{}, err
	}
	z, err := o.connect(ctx)
	if err != nil {
		return Gap{}, err
	}
	defer z.Close(ctx)
	hosts, err := z.ListHosts(ctx)
	if err != nil {
		return Gap{}, err
	}
	return plan.CoverageGap(devices, hosts), nil
}

// InventorySync is the plan for a sync and, when it was applied, what was done.
type InventorySync struct {
	Plan    SyncPlan
	Applied *Applied // nil on a dry run
}

func (s InventorySync) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"plan": s.Plan, "applied": s.Applied})
}

// SyncInventory plans, and with apply creates and updates, the Zabbix hosts for the devices. It never deletes.
func SyncInventory(ctx context.Context, networkID string, apply bool, o Options) (InventorySync, error) {
	devices, err := Devices(ctx, networkID, o)
	if err != nil {
		return InventorySync{}, err
	}
	z, err := o.connect(ctx)
	if err != nil {
		return InventorySync{}, err
	}
	defer z.Close(ctx)
	hosts, err := z.ListHosts(ctx)
	if err != nil {
		return InventorySync{}, err
	}
	res := InventorySync{Plan: plan.PlanSync(devices, hosts, networkID)}
	if apply {
		a, err := z.ApplyPlan(ctx, res.Plan)
		if err != nil {
			return res, err
		}
		res.Applied = &a
	}
	return res, nil
}

// StatusPush is the Forward health values for a network and, when pushed, what Zabbix did with them.
type StatusPush struct {
	SnapshotID         string
	Devices            int
	Values             []Value
	CollectionProblems [][2]string // (device, result)
	Pushed             bool
	Processed          int
	Rejected           int
	SkippedHosts       []string
}

func (s StatusPush) MarshalJSON() ([]byte, error) {
	nz := func(x []string) []string {
		if x == nil {
			return []string{}
		}
		return x
	}
	vals := s.Values
	if vals == nil {
		vals = []Value{}
	}
	cp := s.CollectionProblems
	if cp == nil {
		cp = [][2]string{}
	}
	return json.Marshal(map[string]any{"snapshot_id": s.SnapshotID, "devices": s.Devices, "values": vals,
		"collection_problems": cp, "pushed": s.Pushed, "processed": s.Processed, "rejected": s.Rejected,
		"skipped_hosts": nz(s.SkippedHosts)})
}

// CollectStatus reads Forward's devices and newest snapshot and returns the trapper values to push.
func CollectStatus(ctx context.Context, networkID string, o Options) ([]Device, string, []Value, error) {
	c, err := o.forwardClient()
	if err != nil {
		return nil, "", nil, err
	}
	devices, err := forward.Devices(ctx, c, networkID)
	if err != nil {
		return nil, "", nil, err
	}
	snap, err := forward.NewestSnapshot(ctx, c, networkID)
	if err != nil {
		return nil, "", nil, err
	}
	return devices, snap.ID, plan.StatusValues(devices, networkID, snap.ID, snap.Epoch), nil
}

// SendValues pushes the values that have an item in Zabbix and returns (processed, rejected, skipped hosts).
func SendValues(ctx context.Context, networkID string, values []Value, o Options) (int, int, []string, error) {
	cfg, err := o.zabbixCfg()
	if err != nil {
		return 0, 0, nil, err
	}
	z, err := zabbix.Connect(ctx, cfg)
	if err != nil {
		return 0, 0, nil, err
	}
	defer z.Close(ctx)
	if _, err := z.EnsureNetworkHost(ctx, networkID); err != nil {
		return 0, 0, nil, err
	}
	zv := make([]zabbix.Value, len(values))
	for i, v := range values {
		zv[i] = zabbix.Value{Host: v.Host, Key: v.Key, Value: v.Value}
	}
	have, err := z.ExistingTargets(ctx, zv)
	if err != nil {
		return 0, 0, nil, err
	}
	var sendable []zabbix.Value
	skipped := map[string]bool{}
	for _, v := range zv {
		if have[[2]string{v.Host, v.Key}] {
			sendable = append(sendable, v)
		} else {
			skipped[v.Host] = true
		}
	}
	var names []string
	for h := range skipped {
		names = append(names, h)
	}
	sort.Strings(names)
	if len(sendable) == 0 {
		return 0, 0, names, nil
	}
	processed, failed, err := z.PushValues(ctx, sendable)
	return processed, failed, names, err
}

// PushStatus collects Forward's per-device collection results and newest snapshot; with apply it pushes them.
func PushStatus(ctx context.Context, networkID string, apply bool, o Options) (StatusPush, error) {
	devices, snapshotID, values, err := CollectStatus(ctx, networkID, o)
	if err != nil {
		return StatusPush{}, err
	}
	res := StatusPush{SnapshotID: snapshotID, Devices: len(devices), Values: values}
	for _, v := range values {
		if v.Key == "fwd.collection.result" && v.Value != "completed" {
			res.CollectionProblems = append(res.CollectionProblems, [2]string{v.Host, v.Value})
		}
	}
	if apply {
		res.Processed, res.Rejected, res.SkippedHosts, err = SendValues(ctx, networkID, values, o)
		res.Pushed = err == nil
	}
	return res, err
}

// Note is a Forward note for an open Zabbix problem.
type Note struct {
	EventID string
	Problem string
	Text    string
	Added   bool
}

func (n Note) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"eventid": n.EventID, "problem": n.Problem, "text": n.Text, "added": n.Added})
}

// Enrich builds notes for open problems on managed hosts that have none yet; with apply it adds them.
func Enrich(ctx context.Context, networkID string, apply bool, o Options) ([]Note, error) {
	c, err := o.forwardClient()
	if err != nil {
		return nil, err
	}
	devices, err := forward.Devices(ctx, c, networkID)
	if err != nil {
		return nil, err
	}
	links, err := forward.Links(ctx, c, networkID)
	if err != nil {
		return nil, err
	}
	snap, err := forward.NewestSnapshot(ctx, c, networkID)
	if err != nil {
		return nil, err
	}
	byHost := map[string]Device{}
	for _, d := range devices {
		byHost[strings.ToLower(plan.TechnicalName(d.Name))] = d
	}
	z, err := o.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer z.Close(ctx)
	problems, err := z.OpenProblems(ctx)
	if err != nil {
		return nil, err
	}
	notes := []Note{}
	for _, p := range problems {
		d, ok := byHost[strings.ToLower(p.Host)]
		if p.Noted || !ok {
			continue
		}
		n := Note{EventID: p.EventID, Problem: p.Name, Text: plan.EnrichmentNote(d, links[d.Name], snap.ID)}
		if apply {
			if err := z.AddNote(ctx, p.EventID, n.Text); err != nil {
				return notes, err
			}
			n.Added = true
		}
		notes = append(notes, n)
	}
	return notes, nil
}

// StatusColumns are the columns of ExportStatus rows, in order.
var StatusColumns = plan.StatusColumns

// ExportStatus returns one row per Forward device with what Zabbix says about it. Read-only. Upload the rows to
// Forward as a data file so NQE can join on `device`; this function does not write to Forward.
func ExportStatus(ctx context.Context, networkID string, o Options) ([]map[string]string, error) {
	devices, err := Devices(ctx, networkID, o)
	if err != nil {
		return nil, err
	}
	z, err := o.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer z.Close(ctx)
	hosts, err := z.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := z.ProblemCounts(ctx)
	if err != nil {
		return nil, err
	}
	return plan.StatusRows(devices, hosts, counts), nil
}

// PerfExport is a PerfDataPB built from Zabbix's stored SNMP history for one device. Nothing is uploaded.
type PerfExport struct {
	Device     string    `json:"device"`
	ZabbixHost string    `json:"zabbix_host"`
	VendorOS   string    `json:"vendor_os"`
	Ticks      int       `json:"ticks"`
	Interfaces []string  `json:"interfaces"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	HasCPU     bool      `json:"has_cpu"`
	HasMemory  bool      `json:"has_memory"`
	Bytes      []byte    `json:"-"` // the serialized PerfDataPB
}

// ExportPerf builds the payload Forward's performance pipeline ingests (a PerfDataPB) from the last `window` of
// Zabbix SNMP history for one Forward device. It reads Forward and Zabbix and writes nothing to either.
func ExportPerf(ctx context.Context, networkID, deviceName string, window time.Duration, o Options) (PerfExport, error) {
	devices, err := Devices(ctx, networkID, o)
	if err != nil {
		return PerfExport{}, err
	}
	var dev *Device
	for i := range devices {
		if devices[i].Name == deviceName {
			dev = &devices[i]
		}
	}
	if dev == nil {
		return PerfExport{}, fmt.Errorf("Forward network %s has no device %q", networkID, deviceName)
	}
	vos, ok := perf.VendorOS[dev.OS]
	if !ok {
		return PerfExport{}, fmt.Errorf("device %q has OS %q, which is not a Forward VendorOsPB value", deviceName, dev.OS)
	}
	z, err := o.connect(ctx)
	if err != nil {
		return PerfExport{}, err
	}
	defer z.Close(ctx)
	to := time.Now()
	host := plan.TechnicalName(dev.Name)
	hist, err := z.DeviceHistory(ctx, host, to.Add(-window), to)
	if err != nil {
		return PerfExport{}, err
	}
	hist.Name, hist.VendorOS = dev.Name, vos
	ticks := perf.Build(hist)
	if len(ticks) == 0 {
		return PerfExport{}, fmt.Errorf("Zabbix host %q has no SNMP uptime history in the last %s: is an SNMP template linked and polling?", host, window)
	}
	res := PerfExport{Device: dev.Name, ZabbixHost: host, VendorOS: dev.OS, Ticks: len(ticks),
		From: ticks[0].Time, To: ticks[len(ticks)-1].Time, HasCPU: len(hist.CPU) > 0, HasMemory: len(hist.Memory) > 0}
	for _, s := range hist.Ifaces {
		res.Interfaces = append(res.Interfaces, s.Name)
	}
	res.Bytes = perf.Payload{Time: res.To, Devices: ticks}.Encode()
	return res, nil
}
