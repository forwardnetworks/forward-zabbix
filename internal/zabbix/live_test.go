//go:build live

// Integration tests against a real Zabbix 7.0. Run with:
//
//	ZABBIX_URL=... ZABBIX_USER=... ZABBIX_PASSWORD=... go test -tags live ./internal/zabbix
//
// Use a throwaway Zabbix. FWDZBX_LIVE_WRITE=1 also adds a note to one open problem.
package zabbix

import (
	"context"
	"os"
	"testing"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
)

func TestLiveReadsAndNote(t *testing.T) {
	cfg, err := config.ZabbixFromEnv()
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	c, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	hosts, err := c.ListHosts(ctx)
	if err != nil || len(hosts) == 0 {
		t.Fatalf("hosts %d err %v", len(hosts), err)
	}
	if _, err := c.ProblemCounts(ctx); err != nil {
		t.Fatal(err)
	}
	problems, err := c.OpenProblems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FWDZBX_LIVE_WRITE") != "1" {
		return
	}
	for _, p := range problems {
		if p.Noted {
			continue
		}
		msg := plan.NotePrefix + " live test note"
		if err := c.AddNote(ctx, p.EventID, msg); err != nil {
			t.Fatal(err)
		}
		again, err := c.OpenProblems(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range again {
			if q.EventID == p.EventID && !q.Noted {
				t.Fatalf("note not visible on event %s", p.EventID)
			}
		}
		return
	}
	t.Skip("no un-noted problem to write a note to")
}
