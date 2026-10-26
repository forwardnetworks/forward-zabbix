# Usage

All commands except `serve` take `--network-id ID` (or `FORWARD_NETWORK_ID`) and `--json`, both before the command. Commands that write take
`--apply`; without it they report what they would do. Exit codes: `0` ok, `1` a check you asked for failed
(`coverage-gap --fail-on-gap`, or `push-status` when Zabbix rejected values), `3` a runtime error, `64` configuration or usage error (missing variable, bad flag).

| Command | Reads | Writes (with `--apply`) |
|---|---|---|
| `devices` | Forward | nothing |
| `coverage-gap [--fail-on-gap]` | Forward, Zabbix | nothing |
| `sync-inventory` | Forward, Zabbix | Zabbix hosts, groups, tags, template links |
| `export-status [-o FILE]` | Forward, Zabbix | nothing (a local CSV) |
| `push-status` | Forward, Zabbix | Zabbix trapper values; creates the network host and `Forward` group if missing |
| `enrich` | Forward, Zabbix | acknowledgement notes on open Forward problems |
| `export-perf --device NAME -o FILE [--minutes N]` | Forward, Zabbix | nothing (a local PerfDataPB file; see [Performance data](performance.md)) |
| `upload-perf --device NAME [--minutes N] [--apply] [--allow-existing]` | Forward, Zabbix | Forward performance data, as the registered collector (dry run without `--apply`; see [Performance data](performance.md)) |
| `serve` | | Zabbix trapper values, on each webhook |

## coverage-gap

Compares the two inventories and reports:

- **unmonitored**: in Forward, no Zabbix host (by name, or by IP). "Monitored" here means *a Zabbix host exists*; whether Zabbix polls it depends on the templates you link (this tool links none that poll)
- **unmodeled**: a Zabbix host Forward has no device for
- **drift**: matched, but the name or IP differs
- `matched`: how many agree

`--fail-on-gap` exits 1 when any of the three lists is non-empty, which makes it a CI gate.
Hosts in the `Zabbix servers` group and the network carrier host are ignored.

## sync-inventory

Dry run prints `create`, `update`, `unchanged`, `collisions`, `stale`, `no-ip`. With `--apply` it creates
and updates hosts. It is idempotent: a second run reports everything unchanged.

### Ownership rules

- It manages only hosts carrying the tag `managed-by: forward-zabbix`.
- A device whose name or IP already belongs to another host (one this tool does not manage, one it manages for a different network, or its own old host after a rename in Forward) is a *collision*: reported, never touched. A device renamed in Forward therefore shows as a collision and its old host as stale until you remove the old host.
- On update, a managed host's groups and tags are set to what Forward implies, so a group or tag added by hand to a managed host is removed on the next sync. Two devices whose names map to the same Zabbix host name are not handled; rename one.
- A managed host with no matching device is *stale*: reported, never deleted.
- Template changes only add; a template you linked by hand stays.
- A device with no management IP is reported as `no-ip` and skipped.

## export-status

One CSV row per Forward device describing what Zabbix says about it:
`device,monitored,zabbix_host,open_problems,worst_severity`. A device is matched to a Zabbix host by name,
else by IP, as in `coverage-gap`; `monitored` is `yes` when a Zabbix host exists for the device and `no` when there is none; it says nothing about polling. It is read-only and writes only
the file you name (or stdout). This is the Zabbix-to-Forward direction: upload the file to Forward as a data
file and NQE can join on `device`, for example to list devices that are unmonitored or that have open
Zabbix problems. See [Zabbix data in NQE](forward-setup.md#zabbix-data-in-nqe).

## push-status

Pushes each device's collection result, and the network's counts, newest snapshot id and time. Raises or
clears the `Forward could not collect` problems through the template triggers. Hosts that do not exist in
Zabbix yet are named and skipped, not failed. New hosts take up to about a minute before the server accepts
values for them (its configuration cache).

## enrich

For each open problem on a managed host, adds the note shown in [Zabbix setup](zabbix-setup.md#the-enrichment-note).

## serve

```bash
export FWDZBX_WEBHOOK_USER=forward FWDZBX_WEBHOOK_PASSWORD=...   FWDZBX_NETWORK_IDS=123456
fwdzbx serve --listen 127.0.0.1:8020
```

| Request | Answer |
|---|---|
| no or wrong basic auth | `401` |
| other path or method | `404` |
| body over the limit, or not JSON | `413`, `400` |
| event for a network not in `FWDZBX_NETWORK_IDS` | `200`, ignored |
| `SNAPSHOT_READY` for a listed network | `202`, then status is pushed |
| a check-failure event | `202`, then `fwd.check.failure` is set (not exercised against real Forward, see [Validation](validation.md)) |

Repeated events for the same snapshot are de-duplicated. Headers and bodies are never logged.

## Example: machine-readable

```bash
fwdzbx --json --network-id 123456 coverage-gap | jq '.unmonitored'
fwdzbx --network-id 123456 coverage-gap --fail-on-gap || echo "inventories disagree"
```
