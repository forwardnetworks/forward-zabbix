// The JSON contract pipelines depend on: stdout is valid JSON only, and exit codes mean what the docs say.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-zabbix"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

type fake struct {
	gap          fwdzbx.Gap
	push         fwdzbx.StatusPush
	sync         fwdzbx.InventorySync
	err          error
	devices      []fwdzbx.Device
	topoFailures []string
	evidence     fwdzbx.Evidence
	evidenceReq  fwdzbx.EvidenceRequest
	servicesFail []string
}

func (f *fake) Devices(context.Context, string) ([]fwdzbx.Device, error) { return f.devices, f.err }
func (f *fake) CoverageGap(context.Context, string) (fwdzbx.Gap, error)  { return f.gap, f.err }
func (f *fake) SyncInventory(context.Context, string, bool, bool) (fwdzbx.InventorySync, error) {
	return f.sync, f.err
}
func (f *fake) SyncTopology(_ context.Context, _ string, v []string, apply bool) (fwdzbx.TopologySync, error) {
	r := fwdzbx.TopologySync{Plan: fwdzbx.TopologyPlan{Vantages: v}}
	if apply {
		r.Applied = &fwdzbx.AppliedTopology{Failures: f.topoFailures}
	}
	return r, f.err
}

func (f *fake) ChangeEvidence(_ context.Context, _ string, r fwdzbx.EvidenceRequest) (fwdzbx.Evidence, error) {
	f.evidenceReq = r
	return f.evidence, f.err
}

func (f *fake) SyncServices(_ context.Context, _ string, v []string, apply bool) (fwdzbx.ServicesSync, error) {
	r := fwdzbx.ServicesSync{Plan: fwdzbx.ServicesPlan{Vantages: v}}
	if apply {
		r.Applied = &fwdzbx.AppliedServices{Failures: f.servicesFail}
	}
	return r, f.err
}

func (f *fake) PushStatus(context.Context, string, bool) (fwdzbx.StatusPush, error) {
	return f.push, f.err
}
func (f *fake) SendValues(context.Context, string, []fwdzbx.Value) (int, int, []string, error) {
	return 0, 0, nil, f.err
}
func (f *fake) Enrich(context.Context, string, bool) ([]fwdzbx.Note, error) { return nil, f.err }
func (f *fake) ExportStatus(context.Context, string) ([]map[string]string, error) {
	return []map[string]string{{"device": "a", "monitored": "yes", "zabbix_host": "a", "open_problems": "0", "worst_severity": ""}}, f.err
}

func (f *fake) ExportPerf(context.Context, string, string, time.Duration) (fwdzbx.PerfExport, error) {
	return fwdzbx.PerfExport{Device: "a", Bytes: []byte{1, 2, 3}}, f.err
}

func (f *fake) UploadPerf(_ context.Context, _, _ string, _ time.Duration, apply, _ bool, _ func(string) string) (fwdzbx.PerfUpload, error) {
	return fwdzbx.PerfUpload{Collector: "collector-x", Uploaded: apply, PerfExport: fwdzbx.PerfExport{Device: "a"}}, f.err
}

func (f *fake) PushPerf(_ context.Context, _ string, _ time.Duration, apply, _ bool, _ string, _ func(string) string) (fwdzbx.PerfPush, error) {
	return fwdzbx.PerfPush{Network: "1", Collector: "collector-x", Uploaded: apply, Bytes: 9,
		Devices: []fwdzbx.PerfPushDevice{{Device: "a", Ticks: 2}}, Skipped: map[string]string{"b": "no Zabbix host"}}, f.err
}

func run(f *fake, env map[string]string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb, f, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func dev() plan.Device {
	return plan.Device{Name: "a", Site: "s", Vendor: "V", Model: "m", OS: "o", OSVersion: "1", DeviceType: "SWITCH", IPs: []string{"10.0.0.1"}}
}

func TestResultsSerialiseToPlainJSONIncludingSetsAndNestedValues(t *testing.T) {
	f := &fake{sync: fwdzbx.InventorySync{Plan: plan.SyncPlan{Create: []plan.Desired{*plan.DesiredHost(dev(), "1")}}}}
	code, out, _ := run(f, nil, "--network-id", "1", "--json", "sync-inventory")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		Plan struct {
			Create []struct {
				Host   string   `json:"host"`
				Groups []string `json:"groups"`
			} `json:"create"`
			Stale []string `json:"stale"`
		} `json:"plan"`
		Applied any `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	c := got.Plan.Create[0]
	if c.Host != "a" || got.Applied != nil || got.Plan.Stale == nil || len(c.Groups) != 3 || c.Groups[0] != "Forward" {
		t.Fatalf("got %+v", got)
	}
}

func TestJSONFlagPrintsOnlyJSONOnStdout(t *testing.T) {
	f := &fake{gap: fwdzbx.Gap{Unmonitored: []string{"x"}, Matched: 3}}
	code, out, errb := run(f, nil, "--network-id", "1", "--json", "coverage-gap")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["unmonitored"].([]any)[0] != "x" || errb != "" {
		t.Fatalf("exit %d out %q err %q", code, out, errb)
	}
}

func TestFailOnGapExits1OnlyWhenThereIsAGap(t *testing.T) {
	if code, _, _ := run(&fake{gap: fwdzbx.Gap{Unmonitored: []string{"x"}}}, nil, "--network-id", "1", "coverage-gap", "--fail-on-gap"); code != 1 {
		t.Errorf("gap: exit %d, want 1", code)
	}
	if code, _, _ := run(&fake{gap: fwdzbx.Gap{Matched: 2}}, nil, "--network-id", "1", "coverage-gap", "--fail-on-gap"); code != 0 {
		t.Errorf("clean: exit %d, want 0", code)
	}
	if code, _, _ := run(&fake{gap: fwdzbx.Gap{Unmonitored: []string{"x"}}}, nil, "--network-id", "1", "coverage-gap"); code != 0 {
		t.Errorf("gap without the flag: exit %d, want 0", code)
	}
}

func TestPushStatusExitCodeIs1WhenZabbixRejectsValues(t *testing.T) {
	f := &fake{push: fwdzbx.StatusPush{SnapshotID: "9", Devices: 2, Pushed: true, Processed: 1, Rejected: 1}}
	if code, _, _ := run(f, nil, "--network-id", "1", "push-status", "--apply"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestMissingNetworkIDIsAUsageErrorNotATraceback(t *testing.T) {
	code, _, errb := run(&fake{}, nil, "coverage-gap")
	if code != 64 || !strings.Contains(errb, "network-id") {
		t.Fatalf("exit %d err %q", code, errb)
	}
	if code, _, _ := run(&fake{}, map[string]string{"FORWARD_NETWORK_ID": "5"}, "coverage-gap"); code == 64 {
		t.Fatal("FORWARD_NETWORK_ID was not honoured")
	}
}

func TestRuntimeErrorsExit3AndAreDistinctFromAFailedCheck(t *testing.T) {
	code, out, errb := run(&fake{err: errors.New("boom")}, nil, "--network-id", "1", "coverage-gap", "--fail-on-gap")
	if code != 3 || out != "" || !strings.Contains(errb, "boom") {
		t.Fatalf("exit %d out %q err %q", code, out, errb)
	}
}

func TestUnknownCommandAndNoCommandAreUsageErrors(t *testing.T) {
	if code, _, _ := run(&fake{}, nil, "--network-id", "1", "nope"); code != 64 {
		t.Errorf("unknown command: %d", code)
	}
	if code, _, _ := run(&fake{}, nil); code != 64 {
		t.Errorf("no command: %d", code)
	}
}

func TestExportStatusPrintsCSVWithTheDocumentedColumns(t *testing.T) {
	code, out, _ := run(&fake{}, nil, "--network-id", "1", "export-status")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || lines[0] != "device,monitored,zabbix_host,open_problems,worst_severity" || lines[1] != "a,yes,a,0," {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestServeRefusesToStartWithoutCredentialsNetworksOrAValidListenAddress(t *testing.T) {
	env := map[string]string{"FWDZBX_WEBHOOK_USER": "u", "FWDZBX_WEBHOOK_PASSWORD": "p", "FWDZBX_NETWORK_IDS": "1"}
	if code, _, _ := run(&fake{}, nil, "serve"); code != 64 {
		t.Errorf("no credentials: %d", code)
	}
	if code, _, _ := run(&fake{}, map[string]string{"FWDZBX_WEBHOOK_USER": "u", "FWDZBX_WEBHOOK_PASSWORD": "p"}, "serve"); code != 64 {
		t.Errorf("no networks: %d", code)
	}
	if code, _, errb := run(&fake{}, env, "serve", "--listen", "nonsense"); code != 64 || !strings.Contains(errb, "HOST:PORT") {
		t.Errorf("bad listen: %d %q", code, errb)
	}
}

func TestDevicesTableAndPackageExportsTheAPI(t *testing.T) {
	code, out, _ := run(&fake{devices: []plan.Device{dev()}}, nil, "--network-id", "1", "devices")
	if code != 0 || !strings.Contains(out, "10.0.0.1") || !strings.Contains(out, "1 devices") {
		t.Fatalf("exit %d out %q", code, out)
	}
	var _ = []any{fwdzbx.Devices, fwdzbx.CoverageGap, fwdzbx.SyncInventory, fwdzbx.PushStatus, fwdzbx.Enrich, fwdzbx.ExportStatus}
}

func TestExportPerfNeedsADeviceAndAFileAndWritesOnlyThatFile(t *testing.T) {
	if code, _, errb := run(&fake{}, nil, "--network-id", "1", "export-perf"); code != 64 || !strings.Contains(errb, "--device") {
		t.Fatalf("exit %d err %q", code, errb)
	}
	path := t.TempDir() + "/p.pb"
	code, out, _ := run(&fake{}, nil, "--network-id", "1", "export-perf", "--device", "a", "-o", path)
	b, err := os.ReadFile(path)
	if code != 0 || err != nil || !bytes.Equal(b, []byte{1, 2, 3}) || !strings.Contains(out, "nothing was uploaded") {
		t.Fatalf("exit %d err %v out %q", code, err, out)
	}
}

func TestServeRejectsAFlagAfterSpaceSeparatedNetworkIds(t *testing.T) {
	code, _, errb := run(&fake{}, map[string]string{"FWDZBX_WEBHOOK_USER": "u", "FWDZBX_WEBHOOK_PASSWORD": "p"},
		"serve", "--network-ids", "1", "2", "--listen", "127.0.0.1:0")
	if code != 64 || !strings.Contains(errb, "--listen") {
		t.Fatalf("exit %d err %q", code, errb)
	}
}

func TestExportPerfRejectsNonPositiveMinutes(t *testing.T) {
	if code, _, _ := run(&fake{}, nil, "--network-id", "1", "export-perf", "--device", "a", "-o", t.TempDir()+"/p", "--minutes", "0"); code != 64 {
		t.Fatalf("exit %d", code)
	}
}

func TestUploadPerfIsADryRunUnlessApplied(t *testing.T) {
	code, out, _ := run(&fake{}, nil, "--network-id", "1", "upload-perf", "--device", "a")
	if code != 0 || !strings.Contains(out, "would upload") || !strings.Contains(out, "nothing was sent") {
		t.Fatalf("dry run: exit %d out %q", code, out)
	}
	code, out, _ = run(&fake{}, nil, "--network-id", "1", "upload-perf", "--device", "a", "--apply")
	if code != 0 || !strings.HasPrefix(out, "uploaded") {
		t.Fatalf("apply: exit %d out %q", code, out)
	}
	if code, _, _ := run(&fake{}, nil, "--network-id", "1", "upload-perf"); code != 64 {
		t.Fatalf("missing device: exit %d", code)
	}
}

func TestPushPerfIsADryRunUnlessApplied(t *testing.T) {
	code, out, _ := run(&fake{}, nil, "--network-id", "1", "push-perf")
	if code != 0 || !strings.Contains(out, "would send") || !strings.Contains(out, "no state was written") || !strings.Contains(out, "skipped b: no Zabbix host") {
		t.Fatalf("dry run: exit %d out %q", code, out)
	}
	code, out, _ = run(&fake{}, nil, "--network-id", "1", "push-perf", "--apply")
	if code != 0 || !strings.HasPrefix(out, "sent ") {
		t.Fatalf("apply: exit %d out %q", code, out)
	}
	if code, _, _ := run(&fake{}, nil, "--network-id", "1", "push-perf", "--minutes", "0"); code != 64 {
		t.Fatalf("zero minutes: exit %d", code)
	}
}

func TestSyncTopologyNeedsAVantage(t *testing.T) {
	code, _, errb := run(&fake{}, nil, "--network-id", "1", "sync-topology")
	if code != ExitUsage || !strings.Contains(errb, "--vantage") {
		t.Fatalf("exit %d err %q, want a usage error that names --vantage", code, errb)
	}
}

func TestSyncTopologyVantagesFromFlagsOrEnvAndPlainJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		args []string
	}{
		"repeated flag": {nil, []string{"--vantage", "r1", "--vantage", "r2"}},
		"environment":   {map[string]string{"FWDZBX_VANTAGE": "r1, r2"}, nil},
	} {
		args := append([]string{"--network-id", "1", "--json", "sync-topology"}, tc.args...)
		code, out, errb := run(&fake{}, tc.env, args...)
		var got struct {
			Plan struct {
				Vantages  []string `json:"vantages"`
				Change    []any    `json:"change"`
				Unchanged []string `json:"unchanged"`
				Skipped   any      `json:"skipped"`
			} `json:"plan"`
			Applied any `json:"applied"`
		}
		if code != 0 || errb != "" || json.Unmarshal([]byte(out), &got) != nil {
			t.Fatalf("%s: exit %d out %q err %q", name, code, out, errb)
		}
		if strings.Join(got.Plan.Vantages, ",") != "r1,r2" || got.Plan.Change == nil || got.Plan.Unchanged == nil || got.Plan.Skipped == nil || got.Applied != nil {
			t.Errorf("%s: got %+v; lists must be [] not null and a dry run has no applied", name, got)
		}
	}
}

func TestSyncTopologyExitsOneWhenZabbixRefusesADependency(t *testing.T) {
	f := &fake{topoFailures: []string{"b: cycle"}}
	if code, _, _ := run(f, nil, "--network-id", "1", "sync-topology", "--vantage", "a", "--apply"); code != ExitFailed {
		t.Errorf("exit %d, want %d", code, ExitFailed)
	}
	if code, _, _ := run(&fake{}, nil, "--network-id", "1", "sync-topology", "--vantage", "a", "--apply"); code != 0 {
		t.Errorf("clean apply: exit %d, want 0", code)
	}
}

func TestChangeEvidenceFlagsReachTheRequestAndFailOnDiffSetsTheExitCode(t *testing.T) {
	f := &fake{evidence: fwdzbx.Evidence{Config: plan.Judge([]string{"a", "c"}, []string{"a"})}}
	code, out, _ := run(f, nil, "--network-id", "1", "change-evidence", "--maintenance", "win", "--since", "2026-01-01T10:00:00Z",
		"--expect", "a", "--expect", "b", "--fail-on-diff")
	if code != ExitFailed {
		t.Errorf("a change that differs from what was expected must exit %d with --fail-on-diff, got %d", ExitFailed, code)
	}
	r := f.evidenceReq
	if r.Maintenance != "win" || r.Since.IsZero() || strings.Join(r.Expect, ",") != "a,b" {
		t.Errorf("request = %+v", r)
	}
	if !strings.Contains(out, "Changed but not expected") || !strings.Contains(out, "differs from expectation") {
		t.Errorf("output must show the verdict and the unexpected change:\n%s", out)
	}
	if code, _, _ := run(f, nil, "--network-id", "1", "change-evidence", "--maintenance", "win"); code != 0 {
		t.Errorf("without --fail-on-diff a differing change still exits 0, got %d", code)
	}
}

func TestChangeEvidenceRejectsABadTimeAsAUsageError(t *testing.T) {
	code, _, errb := run(&fake{}, nil, "--network-id", "1", "change-evidence", "--since", "yesterday", "--until", "2026-01-01T10:00:00Z")
	if code != ExitUsage || !strings.Contains(errb, "--since") {
		t.Errorf("exit %d err %q, want a usage error naming --since", code, errb)
	}
}

func TestChangeEvidenceJSONIsPlain(t *testing.T) {
	code, out, errb := run(&fake{}, nil, "--network-id", "1", "--json", "change-evidence", "--before", "1", "--after", "2")
	var got map[string]any
	if code != 0 || errb != "" || json.Unmarshal([]byte(out), &got) != nil || got["problems"] == nil {
		t.Fatalf("exit %d out %q err %q", code, out, errb)
	}
}

func TestSyncServicesNeedsAVantageAndEmitsPlainJSON(t *testing.T) {
	if code, _, errb := run(&fake{}, nil, "--network-id", "1", "sync-services"); code != ExitUsage || !strings.Contains(errb, "--vantage") {
		t.Fatalf("exit %d err %q, want a usage error naming --vantage", code, errb)
	}
	code, out, errb := run(&fake{}, map[string]string{"FWDZBX_VANTAGE": "r1"}, "--network-id", "1", "--json", "sync-services")
	var got struct {
		Plan struct {
			Vantages []string `json:"vantages"`
			Create   []any    `json:"create"`
			Stale    []string `json:"stale"`
		} `json:"plan"`
		Applied any `json:"applied"`
	}
	if code != 0 || errb != "" || json.Unmarshal([]byte(out), &got) != nil || got.Plan.Create == nil || got.Plan.Stale == nil || got.Applied != nil || strings.Join(got.Plan.Vantages, ",") != "r1" {
		t.Fatalf("exit %d out %q err %q got %+v", code, out, errb, got)
	}
}

func TestSyncServicesExitsOneWhenZabbixRefusesAService(t *testing.T) {
	if code, _, _ := run(&fake{servicesFail: []string{"x: refused"}}, nil, "--network-id", "1", "sync-services", "--vantage", "a", "--apply"); code != ExitFailed {
		t.Errorf("exit %d, want %d", code, ExitFailed)
	}
}
