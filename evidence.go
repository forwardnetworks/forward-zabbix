package fwdzbx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/forward"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
	"github.com/forwardnetworks/forward-zabbix/internal/zabbix"
)

// EvidenceRequest says which change to gather evidence for. Give one of: two snapshot ids (Before and After), the
// name of a Zabbix maintenance (Maintenance), or a time window (Since and Until). Times given with a maintenance
// replace its own active period. Expect names the devices the change was meant to alter; with a maintenance and no
// Expect, the maintenance's own hosts are the expectation.
type EvidenceRequest struct {
	Before, After string
	Maintenance   string
	Since, Until  time.Time
	Expect        []string
}

// SnapshotRef is a snapshot in an evidence report.
type SnapshotRef struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
}

// Evidence is what a change did, from both tools: what Forward saw change between two snapshots, whether that matches
// what was meant to change, and what Zabbix raised while it happened. It is read-only.
type Evidence struct {
	Network     string                 `json:"network"`
	Maintenance string                 `json:"maintenance"`
	Before      SnapshotRef            `json:"before"`
	After       SnapshotRef            `json:"after"`
	From        time.Time              `json:"window_from"`
	To          time.Time              `json:"window_to"`
	Config      plan.Judgement         `json:"config"`
	Material    map[string]int64       `json:"material"`       // Forward diff families that changed, with counts
	MaterialFor map[string]int64       `json:"material_files"` // file types that changed, devices per type
	Checks      map[string]int64       `json:"checks"`         // check types whose result changed
	Incomplete  []string               `json:"incomplete"`     // counts that are a lower bound
	Errors      map[string]string      `json:"errors"`         // families Forward could not compute
	Problems    []zabbix.WindowProblem `json:"problems"`       // opened in the window on hosts this tool manages
	Notes       []string               `json:"notes"`
}

// Differs reports whether the change did not match what was meant to change.
func (e Evidence) Differs() bool { return e.Config.Verdict == plan.VerdictDiffers }

func (e Evidence) MarshalJSON() ([]byte, error) {
	type plain Evidence
	n := plain(e)
	if n.Material == nil {
		n.Material = map[string]int64{}
	}
	if n.MaterialFor == nil {
		n.MaterialFor = map[string]int64{}
	}
	if n.Checks == nil {
		n.Checks = map[string]int64{}
	}
	if n.Errors == nil {
		n.Errors = map[string]string{}
	}
	for _, l := range []*[]string{&n.Incomplete, &n.Notes, &n.Config.Changed, &n.Config.Expected, &n.Config.Unexpected, &n.Config.Missing} {
		if *l == nil {
			*l = []string{}
		}
	}
	if n.Problems == nil {
		n.Problems = []zabbix.WindowProblem{}
	}
	return json.Marshal(n)
}

// evidenceMode is how the snapshot pair is found.
type evidenceMode int

const (
	byIDs evidenceMode = iota
	byWindow
)

// evidenceShape rejects a request that does not name the change in exactly one way. It needs no lookups, so it runs
// before anything is read.
func evidenceShape(r EvidenceRequest) error {
	ids := r.Before != "" || r.After != ""
	window := !r.Since.IsZero() || !r.Until.IsZero()
	switch {
	case ids && (r.Before == "" || r.After == ""):
		return errors.New("give both --before and --after")
	case ids && (r.Maintenance != "" || window):
		return errors.New("--before/--after cannot be combined with --maintenance, --since or --until")
	case ids:
		return nil
	case r.Maintenance == "" && !window:
		return errors.New("give --before and --after, or --maintenance NAME, or --since and --until")
	case r.Maintenance == "" && r.Since.IsZero() != r.Until.IsZero():
		return errors.New("give both --since and --until")
	}
	return nil
}

// evidenceInputs says how to find the snapshots and which window to read problems from. m is the maintenance when
// one was named.
func evidenceInputs(r EvidenceRequest, m *zabbix.Maintenance) (mode evidenceMode, since, until time.Time, err error) {
	if err := evidenceShape(r); err != nil {
		return 0, since, until, err
	}
	if r.Before != "" {
		return byIDs, since, until, nil
	}
	if m != nil {
		since, until = m.Since, m.Till
	}
	if !r.Since.IsZero() {
		since = r.Since
	}
	if !r.Until.IsZero() {
		until = r.Until
	}
	if since.IsZero() || until.IsZero() || !until.After(since) {
		return 0, since, until, fmt.Errorf("the change window %s to %s is empty", since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	}
	return byWindow, since, until, nil
}

// ChangeEvidence gathers the evidence for one change. It reads Forward and Zabbix and changes neither; it does not
// start a collection, so the "after" snapshot has to be one Forward has already taken.
func ChangeEvidence(ctx context.Context, networkID string, r EvidenceRequest, o Options) (Evidence, error) {
	if err := evidenceShape(r); err != nil {
		return Evidence{}, fmt.Errorf("%w: %v", config.ErrConfig, err)
	}
	z, err := o.connect(ctx)
	if err != nil {
		return Evidence{}, err
	}
	defer z.Close(ctx)
	var maint *zabbix.Maintenance
	if r.Maintenance != "" {
		m, err := z.Maintenance(ctx, r.Maintenance)
		if err != nil {
			return Evidence{}, err
		}
		maint = &m
	}
	mode, since, until, err := evidenceInputs(r, maint)
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: %v", config.ErrConfig, err)
	}
	c, err := o.forwardClient()
	if err != nil {
		return Evidence{}, err
	}
	snaps, err := forward.ProcessedSnapshots(ctx, c, networkID)
	if err != nil {
		return Evidence{}, err
	}
	var before, after forward.Snap
	if mode == byIDs {
		if before, err = forward.Find(snaps, r.Before); err == nil {
			after, err = forward.Find(snaps, r.After)
		}
		if err == nil && !after.Time.After(before.Time) {
			err = fmt.Errorf("snapshot %s is not newer than %s", r.After, r.Before)
		}
	} else {
		before, after, err = forward.Around(snaps, since, until)
	}
	if err != nil {
		return Evidence{}, err
	}
	ev := Evidence{Network: networkID, Before: SnapshotRef{before.ID, before.Time}, After: SnapshotRef{after.ID, after.Time}, From: since, To: until}
	if mode == byIDs {
		ev.From, ev.To = before.Time, after.Time
	}
	if maint != nil {
		ev.Maintenance = maint.Name
	}

	changed, err := forward.ConfigChanged(ctx, c, before.ID, after.ID)
	if err != nil {
		return ev, err
	}
	sum, err := forward.Material(ctx, c, before.ID, after.ID)
	if err != nil {
		return ev, err
	}
	ev.Material, ev.MaterialFor, ev.Checks, ev.Incomplete, ev.Errors = sum.Counts, sum.Files, sum.Checks, sum.Incomplete, sum.Errors

	devices, err := forward.Devices(ctx, c, networkID)
	if err != nil {
		return ev, err
	}
	expected := r.Expect
	if len(expected) == 0 && maint != nil {
		var unmatched []string
		expected, unmatched = devicesOfHosts(devices, maint.Hosts)
		if len(expected) == 0 {
			ev.Notes = append(ev.Notes, "the maintenance covers no host that matches a Forward device, so there is no expectation to judge by")
		} else if len(unmatched) > 0 {
			ev.Notes = append(ev.Notes, "maintenance hosts with no matching Forward device, left out of the expectation: "+strings.Join(unmatched, ", "))
		}
	}
	ev.Config = plan.Judge(changed, expected)

	hosts, err := z.ListHosts(ctx)
	if err != nil {
		return ev, err
	}
	ours := map[string]bool{}
	for _, h := range hosts {
		if h.Managed() && !h.IsNetworkHost() && h.TagMap()["fwd-network"] == networkID {
			ours[strings.ToLower(h.Host)] = true
		}
	}
	var hostIDs []string
	for _, h := range hosts {
		if ours[strings.ToLower(h.Host)] {
			hostIDs = append(hostIDs, h.HostID)
		}
	}
	probs, err := z.ProblemsBetween(ctx, ev.From, ev.To, hostIDs)
	if err != nil {
		return ev, err
	}
	gone := 0
	for _, p := range probs {
		if ours[strings.ToLower(p.Host)] {
			ev.Problems = append(ev.Problems, p)
		} else if p.Host == "" {
			gone++ // its trigger no longer exists, so its host cannot be told
		}
	}
	if gone > 0 {
		ev.Notes = append(ev.Notes, fmt.Sprintf("%d problem(s) on triggers that no longer exist were left out", gone))
	}
	ev.Notes = append(ev.Notes, "problems cover hosts this tool manages; Forward compares the two snapshots as collected, so a change made and reverted between them does not appear")
	return ev, nil
}

// devicesOfHosts maps Zabbix host names to the Forward devices they stand for, and returns the host names that
// stand for none.
func devicesOfHosts(devices []Device, hosts []string) (matched, unmatched []string) {
	byName := map[string][]string{}
	for _, d := range devices {
		k := strings.ToLower(plan.TechnicalName(d.Name))
		byName[k] = append(byName[k], d.Name)
	}
	for _, h := range hosts {
		if ds, ok := byName[strings.ToLower(h)]; ok {
			matched = append(matched, ds...)
		} else {
			unmatched = append(unmatched, h)
		}
	}
	sort.Strings(matched)
	return matched, unmatched
}
