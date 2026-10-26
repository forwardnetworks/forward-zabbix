// Package cli is the fwdzbx command line: a thin layer over the fwdzbx package.
package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/forwardnetworks/forward-zabbix"
	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
	"github.com/forwardnetworks/forward-zabbix/internal/receiver"
)

// Exit codes. 1 is reserved for a check you asked for failing (coverage-gap --fail-on-gap, a push Zabbix rejected).
const (
	ExitOK     = 0
	ExitFailed = 1
	ExitError  = 3
	ExitUsage  = 64
)

// API is what the commands call. The real one is the fwdzbx package; tests substitute a fake.
type API interface {
	Devices(ctx context.Context, networkID string) ([]fwdzbx.Device, error)
	CoverageGap(ctx context.Context, networkID string) (fwdzbx.Gap, error)
	SyncInventory(ctx context.Context, networkID string, apply bool) (fwdzbx.InventorySync, error)
	PushStatus(ctx context.Context, networkID string, apply bool) (fwdzbx.StatusPush, error)
	SendValues(ctx context.Context, networkID string, values []fwdzbx.Value) (int, int, []string, error)
	Enrich(ctx context.Context, networkID string, apply bool) ([]fwdzbx.Note, error)
	ExportStatus(ctx context.Context, networkID string) ([]map[string]string, error)
}

// Live is the API backed by the real Forward and Zabbix, configured from the environment.
type Live struct{}

func (Live) Devices(ctx context.Context, n string) ([]fwdzbx.Device, error) {
	return fwdzbx.Devices(ctx, n, fwdzbx.Options{})
}
func (Live) CoverageGap(ctx context.Context, n string) (fwdzbx.Gap, error) {
	return fwdzbx.CoverageGap(ctx, n, fwdzbx.Options{})
}
func (Live) SyncInventory(ctx context.Context, n string, apply bool) (fwdzbx.InventorySync, error) {
	return fwdzbx.SyncInventory(ctx, n, apply, fwdzbx.Options{})
}
func (Live) PushStatus(ctx context.Context, n string, apply bool) (fwdzbx.StatusPush, error) {
	return fwdzbx.PushStatus(ctx, n, apply, fwdzbx.Options{})
}
func (Live) SendValues(ctx context.Context, n string, v []fwdzbx.Value) (int, int, []string, error) {
	return fwdzbx.SendValues(ctx, n, v, fwdzbx.Options{})
}
func (Live) Enrich(ctx context.Context, n string, apply bool) ([]fwdzbx.Note, error) {
	return fwdzbx.Enrich(ctx, n, apply, fwdzbx.Options{})
}
func (Live) ExportStatus(ctx context.Context, n string) ([]map[string]string, error) {
	return fwdzbx.ExportStatus(ctx, n, fwdzbx.Options{})
}

type app struct {
	api    API
	out    io.Writer
	err    io.Writer
	json   bool
	netID  string
	ctx    context.Context
	getenv func(string) string
}

func (a *app) emit(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := a.out.Write(buf.Bytes())
	return err
}

func (a *app) section(title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(a.out, "\n%s (%d):\n", title, len(items))
	for _, i := range items {
		fmt.Fprintf(a.out, "  %s\n", i)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

const usage = `usage: fwdzbx [--network-id ID] [--json] COMMAND [flags]

Forward <-> Zabbix integration (PoC)

commands:
  devices          list Forward devices (read-only)
  coverage-gap     diff Forward inventory against Zabbix hosts (read-only); --fail-on-gap
  sync-inventory   create/update Zabbix hosts from Forward (dry run unless --apply)
  push-status      push Forward collection health to Zabbix trapper items (dry run unless --apply)
  enrich           add Forward context to open Zabbix problems on managed hosts (dry run unless --apply)
  export-status    CSV of what Zabbix says about each Forward device (read-only); -o FILE
  serve            receive Forward webhooks and push the result to Zabbix

global flags:
  --network-id ID  Forward network id (or FORWARD_NETWORK_ID)
  --json           print machine-readable JSON on stdout and nothing else
`

// Run executes one command line and returns the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, api API, getenv func(string) string) int {
	a := &app{api: api, out: stdout, err: stderr, ctx: ctx, getenv: getenv}
	global := flag.NewFlagSet("fwdzbx", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&a.netID, "network-id", "", "")
	global.BoolVar(&a.json, "json", false, "")
	if err := global.Parse(args); err != nil {
		fmt.Fprintf(stderr, "%s\n%s", err, usage)
		return ExitUsage
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	cmd, cargs := rest[0], rest[1:]
	var code int
	var err error
	switch cmd {
	case "devices":
		code, err = a.devices(cargs)
	case "coverage-gap":
		code, err = a.coverageGap(cargs)
	case "sync-inventory":
		code, err = a.syncInventory(cargs)
	case "push-status":
		code, err = a.pushStatus(cargs)
	case "enrich":
		code, err = a.enrich(cargs)
	case "export-status":
		code, err = a.exportStatus(cargs)
	case "serve":
		code, err = a.serve(cargs)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", cmd, usage)
		return ExitUsage
	}
	if err != nil {
		if errors.Is(err, config.ErrConfig) {
			fmt.Fprintf(stderr, "config error: %s\n", strings.TrimPrefix(err.Error(), config.ErrConfig.Error()+": "))
			return ExitUsage
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitError
	}
	return code
}

func (a *app) network() (string, error) {
	if a.netID != "" {
		return a.netID, nil
	}
	if v := a.getenv("FORWARD_NETWORK_ID"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w: pass --network-id or set FORWARD_NETWORK_ID", config.ErrConfig)
}

func sub(name string, args []string, setup func(*flag.FlagSet)) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	setup(fs)
	return fs, fs.Parse(args)
}

func usageErr(err error) error { return fmt.Errorf("%w: %v", config.ErrConfig, err) }

func (a *app) devices(args []string) (int, error) {
	if _, err := sub("devices", args, func(*flag.FlagSet) {}); err != nil {
		return 0, usageErr(err)
	}
	net, err := a.network()
	if err != nil {
		return 0, err
	}
	devices, err := a.api.Devices(a.ctx, net)
	if err != nil {
		return 0, err
	}
	if a.json {
		return 0, a.emit(devices)
	}
	for _, d := range devices {
		fmt.Fprintf(a.out, "%-28s %-14s %-10s %-12s %-16s %s\n", d.Name, dash(d.Site), d.Vendor, d.Model, dash(strings.Join(d.IPs, ",")), d.Collected)
	}
	fmt.Fprintf(a.out, "\n%d devices\n", len(devices))
	return 0, nil
}

func (a *app) coverageGap(args []string) (int, error) {
	var failOnGap bool
	if _, err := sub("coverage-gap", args, func(fs *flag.FlagSet) { fs.BoolVar(&failOnGap, "fail-on-gap", false, "") }); err != nil {
		return 0, usageErr(err)
	}
	net, err := a.network()
	if err != nil {
		return 0, err
	}
	gap, err := a.api.CoverageGap(a.ctx, net)
	if err != nil {
		return 0, err
	}
	clean := len(gap.Unmonitored) == 0 && len(gap.Unmodeled) == 0 && len(gap.Drift) == 0
	if a.json {
		if err := a.emit(gap); err != nil {
			return 0, err
		}
	} else {
		fmt.Fprintf(a.out, "matched in Zabbix: %d\n", gap.Matched)
		a.section("In Forward, not monitored in Zabbix", gap.Unmonitored)
		a.section("In Zabbix, not modeled in Forward", gap.Unmodeled)
		var drift []string
		for _, d := range gap.Drift {
			drift = append(drift, d.Device+": "+d.What)
		}
		a.section("In both, but different", drift)
	}
	if clean || !failOnGap {
		return 0, nil
	}
	return ExitFailed, nil
}

func applyFlag(name string, args []string) (bool, error) {
	var apply bool
	_, err := sub(name, args, func(fs *flag.FlagSet) { fs.BoolVar(&apply, "apply", false, "") })
	if err != nil {
		return false, usageErr(err)
	}
	return apply, nil
}

func (a *app) syncInventory(args []string) (int, error) {
	apply, err := applyFlag("sync-inventory", args)
	if err != nil {
		return 0, err
	}
	net, err := a.network()
	if err != nil {
		return 0, err
	}
	res, err := a.api.SyncInventory(a.ctx, net, apply)
	if err != nil {
		return 0, err
	}
	if a.json {
		return 0, a.emit(res)
	}
	p := res.Plan
	fmt.Fprintf(a.out, "create %d  update %d  unchanged %d  collisions %d  stale %d  no-ip %d\n",
		len(p.Create), len(p.Update), len(p.Unchanged), len(p.Collisions), len(p.Stale), len(p.SkippedNoIP))
	var create, update, coll []string
	for _, d := range p.Create {
		create = append(create, d.Host)
	}
	for _, u := range p.Update {
		update = append(update, u.Existing.Host+": "+strings.Join(u.Changes, ", "))
	}
	for _, c := range p.Collisions {
		coll = append(coll, c.Device+": "+c.Reason)
	}
	a.section("Would create", create)
	a.section("Would update", update)
	a.section("Collisions (left untouched)", coll)
	a.section("Stale: managed hosts with no Forward device (never deleted)", p.Stale)
	a.section("Skipped, no management IP", p.SkippedNoIP)
	if apply {
		fmt.Fprintf(a.out, "\napplied: %+v\n", *res.Applied)
	} else {
		fmt.Fprintln(a.out, "\ndry run; pass --apply to make these changes")
	}
	return 0, nil
}

func (a *app) pushStatus(args []string) (int, error) {
	apply, err := applyFlag("push-status", args)
	if err != nil {
		return 0, err
	}
	net, err := a.network()
	if err != nil {
		return 0, err
	}
	res, err := a.api.PushStatus(a.ctx, net, apply)
	if err != nil {
		return 0, err
	}
	code := 0
	if res.Rejected > 0 {
		code = ExitFailed
	}
	if a.json {
		return code, a.emit(res)
	}
	fmt.Fprintf(a.out, "snapshot %s   devices %d   values %d   collection problems %d\n", res.SnapshotID, res.Devices, len(res.Values), len(res.CollectionProblems))
	var probs []string
	for _, p := range res.CollectionProblems {
		probs = append(probs, p[0]+": "+p[1])
	}
	a.section("Collection problems", probs)
	if !apply {
		fmt.Fprintln(a.out, "\ndry run; pass --apply to push these to Zabbix")
		return code, nil
	}
	a.section("Skipped: no such item in Zabbix (host not synced, or Forward Device template not linked)", res.SkippedHosts)
	fmt.Fprintf(a.out, "\npushed: processed %d, rejected by the server %d\n", res.Processed, res.Rejected)
	if res.Rejected > 0 {
		fmt.Fprintln(a.out, "rejected values are usually a host the server has not loaded yet (config cache, up to ~60s): run again shortly")
	}
	return code, nil
}

func (a *app) enrich(args []string) (int, error) {
	apply, err := applyFlag("enrich", args)
	if err != nil {
		return 0, err
	}
	net, err := a.network()
	if err != nil {
		return 0, err
	}
	notes, err := a.api.Enrich(a.ctx, net, apply)
	if err != nil {
		return 0, err
	}
	if a.json {
		return 0, a.emit(notes)
	}
	fmt.Fprintf(a.out, "open problems on Forward-managed hosts needing a note: %d\n", len(notes))
	for _, n := range notes {
		fmt.Fprintf(a.out, "\n--- event %s: %s\n%s\n", n.EventID, n.Problem, n.Text)
	}
	if len(notes) > 0 {
		if apply {
			fmt.Fprintln(a.out, "\nadded notes")
		} else {
			fmt.Fprintln(a.out, "\ndry run; pass --apply to add these notes to the problems")
		}
	}
	return 0, nil
}

func (a *app) exportStatus(args []string) (int, error) {
	var output string
	if _, err := sub("export-status", args, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "output", "", "")
		fs.StringVar(&output, "o", "", "")
	}); err != nil {
		return 0, usageErr(err)
	}
	net, err := a.network()
	if err != nil {
		return 0, err
	}
	rows, err := a.api.ExportStatus(a.ctx, net)
	if err != nil {
		return 0, err
	}
	if a.json {
		return 0, a.emit(rows)
	}
	var w io.Writer = a.out
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		w = f
	}
	cw := csv.NewWriter(w)
	_ = cw.Write(plan.StatusColumns)
	for _, r := range rows {
		rec := make([]string, len(plan.StatusColumns))
		for i, c := range plan.StatusColumns {
			rec[i] = r[c]
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, err
	}
	if output != "" {
		fmt.Fprintf(a.err, "wrote %d rows to %s\n", len(rows), output)
	}
	return 0, nil
}

func (a *app) serve(args []string) (int, error) {
	var listen, path, ids string
	fs, err := sub("serve", args, func(fs *flag.FlagSet) {
		fs.StringVar(&listen, "listen", "127.0.0.1:8020", "")
		fs.StringVar(&path, "path", "/forward-webhook", "")
		fs.StringVar(&ids, "network-ids", "", "")
	})
	if err != nil {
		return 0, usageErr(err)
	}
	// ids may be comma-separated, repeated, or space-separated after the flag
	raw := append([]string{ids}, fs.Args()...)
	if ids == "" && len(fs.Args()) == 0 {
		raw = []string{a.getenv("FWDZBX_NETWORK_IDS")}
	}
	var networks []string
	for _, item := range raw {
		for _, x := range strings.Split(item, ",") {
			if x = strings.TrimSpace(x); x != "" {
				networks = append(networks, x)
			}
		}
	}
	logger := log.New(a.err, "", log.LstdFlags)
	rx, err := receiver.New(a.getenv("FWDZBX_WEBHOOK_USER"), a.getenv("FWDZBX_WEBHOOK_PASSWORD"), networks, path, receiver.Handlers{
		OnSnapshot: func(ctx context.Context, n string) error {
			r, err := a.api.PushStatus(ctx, n, true)
			if err == nil {
				logger.Printf("fwdzbx.receiver pushed status for snapshot %s: processed %d rejected %d skipped %d", r.SnapshotID, r.Processed, r.Rejected, len(r.SkippedHosts))
			}
			return err
		},
		OnValues: func(ctx context.Context, n string, v []plan.Value) error {
			p, r, _, err := a.api.SendValues(ctx, n, v)
			if err == nil {
				logger.Printf("fwdzbx.receiver pushed %d event value(s): processed %d rejected %d", len(v), p, r)
			}
			return err
		}})
	if err != nil {
		return 0, fmt.Errorf("%w: %v (set FWDZBX_WEBHOOK_USER, FWDZBX_WEBHOOK_PASSWORD and --network-ids or FWDZBX_NETWORK_IDS)", config.ErrConfig, err)
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("%w: --listen must be HOST:PORT, got %q", config.ErrConfig, listen)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("%w: --listen must be HOST:PORT, got %q", config.ErrConfig, listen)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(a.out, "listening on %s%s for networks %v\n", listen, path, networks)
	ctx, stop := signal.NotifyContext(a.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return 0, rx.Serve(ctx, ln)
}
