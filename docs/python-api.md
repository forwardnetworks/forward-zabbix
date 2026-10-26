# Python API and pipelines

The CLI is a thin layer over a Python API. Use whichever your pipeline prefers.

## API

```python
import fwdzbx

devices = fwdzbx.devices("123456")                      # list[FwdDevice], from Forward
gap = fwdzbx.coverage_gap("123456")                      # Gap: unmonitored, unmodeled, drift, matched
sync = fwdzbx.sync_inventory("123456")                   # dry run: sync.plan
sync = fwdzbx.sync_inventory("123456", apply=True)       # sync.applied == {"created": n, "updated": n}
push = fwdzbx.push_status("123456", apply=True)          # processed / rejected / skipped_hosts
rows = fwdzbx.export_status("123456")                  # list[dict]: device, monitored, zabbix_host, ...
notes = fwdzbx.enrich("123456", apply=True)              # list[Note]
```

- Configuration comes from the [environment](configuration.md). To pass it explicitly, give
  `zabbix=fwdzbx.config.ZabbixSettings(...)` and, where accepted, `forward=<ForwardClient>`.
- `apply` defaults to `False` everywhere: calling a function never changes Zabbix by accident.
- Failures raise exceptions (`fwdzbx.config.ConfigError` for missing settings); nothing is swallowed.
- `fwdzbx.api.to_jsonable(obj)` turns any result into plain JSON types.

## Use in a pipeline

`examples/pipeline_gate.py` fails a run when Forward models devices Zabbix does not monitor, and shows what a
sync would change without changing it:

```python
gap = fwdzbx.coverage_gap(network_id)
if gap.unmonitored:
    sys.exit(1)
plan = fwdzbx.sync_inventory(network_id).plan   # dry run
```

Run against the test setup it printed `matched=26 unmonitored=0 unmodeled=1 drift=2` and
`a sync would create 0 and update 0 hosts`.

A typical shape is a step after a Forward snapshot in a larger job (CI, Airflow, cron, a change pipeline): call
`sync_inventory(..., apply=True)` then `push_status(..., apply=True)`. Those two calls are plain functions with
no framework dependency, so they fit any scheduler. Only the plain-Python and shell forms above have been run;
framework wrappers are not provided.

## Use the CLI from another tool

`--json` makes stdout one JSON document and nothing else (logs go to stderr):

```bash
fwdzbx --json --network-id 123456 coverage-gap | jq '.unmonitored[]'
fwdzbx --network-id 123456 coverage-gap --fail-on-gap     # exit 1 on any gap
```

The top-level keys of `coverage-gap` are `drift`, `matched`, `unmodeled`, `unmonitored`.
