// Command pipeline_gate shows fwdzbx inside a Go pipeline: fail the run if Forward and Zabbix disagree.
//
//	export FORWARD_URL=... FORWARD_USERNAME=... FORWARD_PASSWORD=...
//	export ZABBIX_URL=... ZABBIX_TOKEN=...
//	go run ./examples/pipeline_gate <forward-network-id>
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	fwdzbx "github.com/forwardnetworks/forward-zabbix"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: pipeline_gate <forward-network-id>")
		os.Exit(64)
	}
	ctx, networkID := context.Background(), os.Args[1]
	gap, err := fwdzbx.CoverageGap(ctx, networkID, fwdzbx.Options{}) // read-only
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(3)
	}
	fmt.Printf("matched=%d unmonitored=%d unmodeled=%d drift=%d\n", gap.Matched, len(gap.Unmonitored), len(gap.Unmodeled), len(gap.Drift))
	if len(gap.Unmonitored) > 0 {
		fmt.Println("not in Zabbix:", strings.Join(gap.Unmonitored, ", "))
		os.Exit(1)
	}
	sync, err := fwdzbx.SyncInventory(ctx, networkID, false, fwdzbx.Options{}) // dry run: nothing is changed
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(3)
	}
	fmt.Printf("a sync would create %d and update %d hosts\n", len(sync.Plan.Create), len(sync.Plan.Update))
}
