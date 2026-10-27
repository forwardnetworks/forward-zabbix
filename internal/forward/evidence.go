package forward

import (
	"context"
	"fmt"
	"sort"
	"time"

	sdk "github.com/forwardnetworks/forward-go-sdk"
)

// Snap is a usable snapshot with its creation time.
type Snap struct {
	ID   string
	Time time.Time
}

// ProcessedSnapshots lists the network's processed snapshots, oldest first. Drafts and predictions are left out, as
// NewestSnapshot does.
func ProcessedSnapshots(ctx context.Context, c *sdk.Client, networkID string) ([]Snap, error) {
	list, _, err := c.Snapshots.List(ctx, networkID, sdk.SnapshotListOptions{})
	if err != nil {
		return nil, err
	}
	return usable(list), nil
}

func usable(list []sdk.Snapshot) []Snap {
	var out []Snap
	for _, s := range list {
		if s.IsDraft || s.IsPredicted || s.State != "PROCESSED" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
		if err != nil {
			continue
		}
		out = append(out, Snap{ID: s.ID.String(), Time: t})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].ID < out[j].ID // a fixed order when two snapshots share a time
	})
	return out
}

// Around picks the evidence pair (snaps oldest first, as ProcessedSnapshots returns them) for a change window: the newest snapshot taken at or before since, and the oldest
// taken at or after until. Forward collects on its own schedule, so the "after" one may not exist yet.
func Around(snaps []Snap, since, until time.Time) (before, after Snap, err error) {
	for _, s := range snaps {
		if !s.Time.After(since) {
			before = s
		}
		if !s.Time.Before(until) && after.ID == "" {
			after = s
		}
	}
	switch {
	case before.ID == "":
		return before, after, fmt.Errorf("no processed snapshot at or before %s", since.UTC().Format(time.RFC3339))
	case after.ID == "":
		return before, after, fmt.Errorf("no processed snapshot at or after %s yet; Forward has not collected since the window ended", until.UTC().Format(time.RFC3339))
	}
	return before, after, nil
}

// Find returns the snapshot with this id from the list.
func Find(snaps []Snap, id string) (Snap, error) {
	for _, s := range snaps {
		if s.ID == id {
			return s, nil
		}
	}
	return Snap{}, fmt.Errorf("snapshot %s is not a processed snapshot of this network", id)
}

// ConfigChanged lists the devices whose collected CONFIG differs between the two snapshots, sorted.
func ConfigChanged(ctx context.Context, c *sdk.Client, before, after string) ([]string, error) {
	diffs, _, err := c.Diffs.Files(ctx, before, after, "CONFIG")
	if err != nil {
		return nil, err
	}
	return configDevices(diffs), nil
}

// configDevices names the devices with at least one changed file, sorted, once each.
func configDevices(diffs []sdk.DeviceFilesDiff) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range diffs {
		if d.Name != "" && len(d.Files) > 0 && !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Material summarises what changed between two snapshots, with state-file churn left out.
func Material(ctx context.Context, c *sdk.Client, before, after string) (sdk.MaterialDiffSummary, error) {
	return c.Diffs.MaterialSummary(ctx, before, after)
}
