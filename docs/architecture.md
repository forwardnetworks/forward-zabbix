# Architecture

```
Forward ---SNAPSHOT_READY webhook (HTTPS, basic auth)---> fwdzbx serve ---+
   ^                                                                       |  Zabbix API (hosts, notes)
   +---- NQE / snapshots (Forward Python SDK) <---- fwdzbx commands -------+- trapper port (values)
                                                                           v
                                                                        Zabbix
```

A standalone Python service. It is not a Zabbix plugin or agent, and not part of Forward.

| Module | Job |
|---|---|
| `forward.py` | Forward reads: devices and links by NQE, newest processed snapshot |
| `plan.py` | Pure logic with no I/O: what to create or update, coverage gap, status values, notes. Most tests live here |
| `zabbix.py` | The Zabbix side: API calls and trapper pushes |
| `service.py` | Collect-then-push, shared by the CLI and the receiver |
| `receiver.py` | The webhook server: auth, parse, de-duplicate, queue, worker |
| `api.py`, `cli.py` | The public Python API and the command line over it |

## Decisions

- **Forward is read-only here.** Nothing changes Forward; the Forward-side setup is yours to do.
- **Plan, then apply.** Logic in `plan.py` returns a plan; `apply` executes it. Dry run is the default.
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
