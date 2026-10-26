# Forward <-> Zabbix: PoC design sketch

Status: design sketch. Built so far: A1 (inventory sync), A2 (snapshot health, by webhook and by polling), parts of B1 (a note on problems) and B5 (a CSV for an NQE data file), and the coverage gap. See the [README](README.md). The rest is not built. Items marked **[verify]** were assumptions when written.

## Framing

- **Forward is the source of truth** for what the network *is*: devices, topology, config, intended policy (checks), and modeled behavior (paths, reachability, change impact).
- **Zabbix is the source of truth** for what the network *is doing right now*: liveness, counters, alarms, operational state, and who acknowledged what.
- Neither monitors the other. The integration is about each one supplying what the other lacks.

| Question | Owner |
|---|---|
| What devices should exist, where, what role? | Forward |
| Is device X up / how hot / dropping packets *now*? | Zabbix |
| Is the config compliant with policy? | Forward |
| Is a device we know about *not monitored*? | Needs both |
| Zabbix alarmed on X. What else is affected? | Needs both |

## Direction A: Forward -> Zabbix

| # | Flow | Mechanism | Value |
|---|---|---|---|
| A1 | **Inventory sync**: Forward devices become Zabbix hosts; site, vendor, role, tags become host groups and host tags | Bridge reads devices via SDK, `host.create/update` via Zabbix API; idempotent, keyed on Forward device name | Zabbix stays populated without hand-built inventory; tags make triggers and dashboards filterable by site/role |
| A2 | **Snapshot health events**: `SNAPSHOT_READY` carries counts of devices with collection/processing failures by error type | Native Forward webhook -> bridge -> trapper items on a "Forward" host (or per-device when known) | Collection failures (auth failed, unreachable) show in the same pane as everything else |
| A3 | **Policy violations as problems**: `INTENT_VERIFICATION_FAILURE`, `NQE_VERIFICATION_FAILURE` | Same webhook path; bridge raises a Zabbix problem via trapper item + trigger, tagged with check name and network | Config/compliance drift pages the same people who get operational alerts |
| A4 | **Check status as metrics**: pass/fail counts per check directory | Bridge polls checks after each snapshot, pushes values | Trend and dashboard compliance over time |
| A5 | **Topology context** (links, upstream/downstream) as host tags or a dependency map | NQE or topology API -> Zabbix trigger dependencies | Parent-down suppresses child alarms: fewer alert storms |

## Direction B: Zabbix -> Forward

| # | Flow | Mechanism | Value |
|---|---|---|---|
| B1 | **Problem enrichment** (headline feature): on a Zabbix problem for device X, attach Forward context: neighbors, blast radius, last config change, failing checks, `investigate-reachability` result | Zabbix webhook media type (JS) -> bridge -> Forward queries -> `event.acknowledge` message back on the problem | On-call sees "what changed and what's downstream" without opening Forward |
| B2 | **Freshness trigger**: interface flap, BGP session change, or config-change event in Zabbix starts a bounded Forward collection | Webhook media type -> bridge -> `startcollection`, with a debounce and cooldown | Forward model tracks reality; keeps collection rate-limited |
| B3 | **Maintenance windows**: Zabbix maintenance start/end takes a before/after Forward snapshot and diffs them | Zabbix action on maintenance -> bridge -> collection + snapshot compare | Automatic change evidence; flags unintended changes |
| B4 | **Onboarding**: Zabbix hosts not in Forward become a candidate device list | Bridge diffs the two sets and emits a proposed `edit-source` plan (dry run until approved) | Closes the gap in the other direction |
| B5 | **Zabbix as NQE data**: monitoring status exposed to Forward queries | Forward HTTP data connector (polled each collection) or uploaded data file from the Zabbix API | NQE can answer "devices not monitored", "Zabbix-down but Forward-compliant" |

## The pairing worth demoing: coverage gap (A1 + B4 + B5)

Diff the two inventories and report:
- In Forward, not in Zabbix: **unmonitored devices**.
- In Zabbix, not in Forward: **unmodeled devices** (monitoring something Forward can't see).
- In both with mismatched site/role/IP: **drift**.

Neither tool can say this alone, and it needs no streaming or alerts, so it is the cheapest convincing PoC.

## Components

```
Forward ---webhook---> [ bridge ] ---Zabbix API / trapper---> Zabbix
   ^                      |  ^                                  |
   +--- SDK queries ------+  +--- Zabbix webhook media type ----+
```

- **bridge** (one small service, Python): webhook receiver (ack fast, queue, no reliance on Forward retries), sync jobs (A1, A4, B4), enrichment endpoint (B1, B2, B3).
- **Zabbix template** (importable YAML): trapper items, triggers, dashboards, value maps.
- **Zabbix media type** (JS webhook script): small; just forwards problems to the bridge.
- State: none required for PoC beyond a cursor/debounce file; Zabbix and Forward remain the stores.

## Design rules

1. Idempotent syncs keyed on Forward device name; never delete Zabbix hosts the bridge did not create (tag `managed-by=forward-zabbix`). 
2. Anything that changes Forward (B2, B4) is rate-limited and dry-run by default.
3. Webhook receiver returns 2xx immediately; Forward webhooks have no retry, so the bridge also polls snapshots as a fallback.
4. Credentials from environment only; Forward via API token; Zabbix via API token.

## Suggested PoC slice (in order)

1. Local Zabbix 7.0 in Docker; bridge skeleton; SDK connectivity to Forward.
2. **A1 inventory sync** + **coverage-gap report** (no webhooks needed, immediately demoable).
3. **A2/A3**: webhook receiver, one `SNAPSHOT_READY` and one check failure becoming Zabbix problems.
4. **B1**: problem enrichment for one device.
5. Stretch: B2/B3.

## Open questions / [verify]

- Zabbix version (7.0 has `history.push`, which simplifies pushes; 6.0 needs the trapper protocol).
- Python SDK coverage of performance data and webhook configuration **[verify]**.
- Whether the hosted Forward can reach the receiver (webhooks are outbound from Forward; needs a reachable URL or a tunnel) **[verify]**. The polling fallback covers this for the PoC.
- Device naming: do Forward device names match what Zabbix hosts will be called?
