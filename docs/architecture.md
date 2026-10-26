# Architecture

```
Forward ---SNAPSHOT_READY webhook (HTTPS, basic auth)---> fwdzbx serve ---+
   ^                                                                       |  Zabbix API (hosts, notes)
   +---- NQE / snapshots (Forward Go SDK) <---- fwdzbx commands -------+- trapper port (values)
                                                                           v
                                                                        Zabbix
```

A standalone Go service, one static binary. It is not a Zabbix plugin or agent, and not part of Forward.

| Module | Job |
|---|---|
| `internal/forward` | Forward reads: devices and links by NQE, newest processed snapshot |
| `internal/plan` | Pure logic with no I/O: what to create or update, coverage gap, status values, notes. Most tests live here |
| `internal/zabbix` | The Zabbix side: JSON-RPC calls, trapper pushes, history reads |
| `internal/receiver` | The webhook server: auth, parse, de-duplicate, queue, worker |
| `internal/perf` | Forward performance payload (PerfDataPB) encoder and rate-to-counter reconstruction |
| `internal/cli`, root package `fwdzbx` | The command line, and the public Go API it sits on |

## Decisions

- **Forward is read-only here.** Nothing changes Forward; the Forward-side setup is yours to do.
- **Plan, then apply.** Logic in `internal/plan` returns a plan; `apply` executes it. Dry run is the default.
- **Ownership by tag.** The `managed-by` tag is the only license to modify a host.
- **Trapper items, not polling.** Forward is not a device to monitor, so values are pushed.
- **Receiver answers first, works later.** Forward gives it 10 seconds and does not retry, so it queues and
  returns at once; the timers cover a missed delivery.
- **Newest snapshot by creation time.** The SDK's latest-processed helper returned an older snapshot in testing,
  so the newest non-draft processed snapshot is chosen from the list.
- **Failure is sticky only for a window.** Forward reports failing checks and never recoveries, so the
  check-failure trigger clears after `{$FWD.CHECK_FAILURE.WINDOW}` without a new failure event.

See [DESIGN.md](../DESIGN.md) for flows not built yet (topology dependencies, maintenance windows, freshness
triggers).
