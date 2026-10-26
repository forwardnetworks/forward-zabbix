# Go API and pipelines

The CLI is a thin layer over a Go package. Use whichever your pipeline prefers.

## API

```go
import fwdzbx "github.com/forwardnetworks/forward-zabbix"

ctx, o := context.Background(), fwdzbx.Options{} // zero Options reads the environment

devices, err := fwdzbx.Devices(ctx, "123456", o)              // from Forward
gap, err := fwdzbx.CoverageGap(ctx, "123456", o)              // Unmonitored, Unmodeled, Drift, Matched
sync, err := fwdzbx.SyncInventory(ctx, "123456", false, o)    // dry run: sync.Plan
sync, err = fwdzbx.SyncInventory(ctx, "123456", true, o)      // sync.Applied: created and updated counts
push, err := fwdzbx.PushStatus(ctx, "123456", true, o)        // processed / rejected / skipped hosts
rows, err := fwdzbx.ExportStatus(ctx, "123456", o)            // []map[string]string
notes, err := fwdzbx.Enrich(ctx, "123456", true, o)           // []Note
```

- Configuration comes from the [environment](configuration.md). To pass it explicitly, fill `Options.Zabbix`
  and `Options.Forward`.
- `apply` is an explicit argument: nothing changes Zabbix unless you pass `true`.
- Failures are returned as errors; `config.ErrConfig` marks missing settings. Nothing is swallowed.

## Use in a pipeline

`examples/pipeline_gate` fails a run when Forward models devices Zabbix does not monitor, and shows what a sync
would change without changing it:

```bash
go run ./examples/pipeline_gate <forward-network-id>
```

A typical shape is a step after a Forward snapshot in a larger job (CI, cron, a change pipeline): call
`SyncInventory(..., true, ...)` then `PushStatus(..., true, ...)`. Those are plain functions with no framework
dependency. The example compiles; it has not been run since the port to Go.

## Use the CLI from another tool

`--json` makes stdout one JSON document and nothing else (logs go to stderr):

```bash
fwdzbx --json --network-id 123456 coverage-gap | jq '.unmonitored[]'
fwdzbx --network-id 123456 coverage-gap --fail-on-gap     # exit 1 on any gap
```

Exit codes: 0 success, 1 a failed check (a gap with `--fail-on-gap`, or values the server rejected), 3 a runtime
error, 64 a usage or configuration error. The top-level keys of `coverage-gap` are `drift`, `matched`,
`unmodeled`, `unmonitored`.
