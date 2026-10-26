package fwdzbx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	sdk "github.com/forwardnetworks/forward-go-sdk"
	"github.com/forwardnetworks/forward-zabbix/internal/perf"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
	"github.com/forwardnetworks/forward-zabbix/internal/zabbix"
)

// existingSamples counts the CPU, memory and interface-utilization rows Forward already holds for a device in the
// last day. Forward's metric reads are per type; any row for the device means something already supplies it.
func existingSamples(ctx context.Context, c *sdk.Client, networkID, deviceName string) (int, error) {
	n := 0
	for _, typ := range []string{"CPU", "MEMORY"} {
		m, _, err := c.Performance.DeviceMetrics(ctx, networkID, sdk.MetricQuery{Type: typ, Days: 1})
		if err != nil {
			return 0, fmt.Errorf("could not check whether Forward already holds performance data for %q: %w", deviceName, err)
		}
		for _, r := range m.Metrics {
			if r.DeviceName == deviceName {
				n++
			}
		}
	}
	im, _, err := c.Performance.InterfaceMetrics(ctx, networkID, sdk.MetricQuery{Type: "UTILIZATION", Days: 1, Direction: "INGRESS"})
	if err != nil {
		return 0, fmt.Errorf("could not check whether Forward already holds performance data for %q: %w", deviceName, err)
	}
	for _, r := range im.Metrics {
		if r.DeviceName == deviceName {
			n++
		}
	}
	return n, nil
}

// PerfState remembers, per device, the newest tick already sent to Forward. Forward silently drops any sample older
// than the newest it holds for a device, so a scheduled upload must send only newer ticks.
type PerfState struct {
	Newest map[string]int64 `json:"newest"` // device name -> unix seconds of the newest tick sent
}

// DefaultPerfStatePath is $STATE_DIRECTORY/perf-state.json under systemd, else a file under the user's state dir.
func DefaultPerfStatePath(getenv func(string) string) string {
	if d := getenv("FWDZBX_STATE_FILE"); d != "" {
		return d
	}
	if d := getenv("STATE_DIRECTORY"); d != "" {
		return filepath.Join(d, "perf-state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "fwdzbx", "perf-state.json")
}

// LoadPerfState reads the state file; a missing file is an empty state, a damaged one is an error (never silently
// reset: that would re-send history or mask a problem).
func LoadPerfState(path string) (PerfState, error) {
	st := PerfState{Newest: map[string]int64{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return PerfState{Newest: map[string]int64{}}, fmt.Errorf("state file %s is damaged (%v): fix or remove it", path, err)
	}
	if st.Newest == nil {
		st.Newest = map[string]int64{}
	}
	return st, nil
}

// Save writes the state atomically with mode 0600.
func (s PerfState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".perf-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// newerThan keeps the ticks strictly after the watermark (unix seconds).
func newerThan(ticks []perf.DevicePerf, watermark int64) []perf.DevicePerf {
	var out []perf.DevicePerf
	for _, t := range ticks {
		if t.Time.Unix() > watermark {
			out = append(out, t)
		}
	}
	return out
}

// PerfPushDevice is one device's part of a push.
type PerfPushDevice struct {
	Device string    `json:"device"`
	Ticks  int       `json:"ticks"`
	Newest time.Time `json:"newest"`
}

// PerfPush is the result of PushPerf. Uploaded is false for a dry run.
type PerfPush struct {
	Network   string            `json:"network"`
	Collector string            `json:"collector"`
	Devices   []PerfPushDevice  `json:"devices"`
	Skipped   map[string]string `json:"skipped"` // device -> why nothing was sent
	Bytes     int               `json:"bytes"`
	Uploaded  bool              `json:"uploaded"`
}

// PushPerf sends every Forward device's new Zabbix SNMP ticks in one upload. It is meant to run every five minutes.
// A device never sent before is first checked against Forward's existing data and skipped if any is found
// (allowExisting overrides). State advances only after Forward accepts the upload; a dry run never writes it.
func PushPerf(ctx context.Context, networkID string, window time.Duration, apply, allowExisting bool, statePath string, o Options, getenv func(string) string) (PerfPush, error) {
	id, err := collectorIdentity(getenv)
	if err != nil {
		return PerfPush{}, err
	}
	res := PerfPush{Network: networkID, Collector: id.Username, Skipped: map[string]string{}}
	st, err := LoadPerfState(statePath)
	if err != nil {
		return res, err
	}
	devices, err := Devices(ctx, networkID, o)
	if err != nil {
		return res, err
	}
	z, err := o.connect(ctx)
	if err != nil {
		return res, err
	}
	defer z.Close(ctx)
	c, err := o.forwardClient()
	if err != nil {
		return res, err
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	to := time.Now()
	var batch []perf.DevicePerf
	newest := map[string]int64{}
	for _, dev := range devices {
		vos, ok := perf.LookupVendorOS(dev.OS)
		if !ok {
			res.Skipped[dev.Name] = "OS " + dev.OS + " is not a Forward vendor OS"
			continue
		}
		hist, err := z.DeviceHistory(ctx, plan.TechnicalName(dev.Name), to.Add(-window), to)
		if errors.Is(err, zabbix.ErrNoHost) {
			res.Skipped[dev.Name] = "no Zabbix host"
			continue
		}
		if err != nil {
			return res, err
		}
		hist.Name, hist.VendorOS = dev.Name, vos
		last, seen := st.Newest[dev.Name]
		ticks := newerThan(perf.Build(hist), last)
		if len(ticks) == 0 {
			res.Skipped[dev.Name] = "no new ticks"
			continue
		}
		if !seen && !allowExisting {
			n, err := existingSamples(ctx, c, networkID, dev.Name)
			if err != nil {
				return res, err
			}
			if n > 0 {
				res.Skipped[dev.Name] = fmt.Sprintf("Forward already holds %d performance row(s) for it; not mixing sources", n)
				continue
			}
		}
		batch = append(batch, ticks...)
		newest[dev.Name] = ticks[len(ticks)-1].Time.Unix()
		res.Devices = append(res.Devices, PerfPushDevice{dev.Name, len(ticks), ticks[len(ticks)-1].Time})
	}
	if len(batch) == 0 {
		return res, nil
	}
	payload := perf.Payload{Time: time.Now(), Devices: batch}.Encode()
	res.Bytes = len(payload)
	if !apply {
		return res, nil
	}
	// one attempt: a failed write's outcome is unknown, and the next run re-sends anything not recorded as sent
	if _, err := c.Performance.UploadWithIdentity(sdk.WithoutRetry(ctx), networkID, id, payload); err != nil {
		return res, fmt.Errorf("upload to Forward failed: %w", err)
	}
	res.Uploaded = true
	for d, t := range newest {
		st.Newest[d] = t
	}
	if err := st.Save(statePath); err != nil {
		return res, fmt.Errorf("uploaded, but could not record it in %s (the next run will re-send the same ticks, which is harmless): %w", statePath, err)
	}
	return res, nil
}
