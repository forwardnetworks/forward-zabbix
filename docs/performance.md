# Performance data (experimental)

Idea: let Zabbix be the SNMP poller and feed its history to Forward's performance views, instead of running a
second SNMP collection.

`fwdzbx export-perf` builds the payload and **uploads nothing**; `fwdzbx upload-perf` does the same and, only with `--apply`, sends it to Forward as the registered collector:

```bash
fwdzbx --network-id <id> export-perf --device <forward device name> --minutes 180 -o perf.pb
```

It reads the device's Zabbix SNMP history and writes a `PerfDataPB`. Two sources work:

- **Recommended: the `Forward Performance by SNMP` template** (`zabbix/forward-performance-template.yaml`, import
  it like the others). It polls the raw cumulative counters Forward wants (ifHCInOctets/OutOctets, errors,
  discards), interface speed and type, uptime, CPU (HOST-RESOURCES-MIB, averaged over cores) and memory
  (hrStorage RAM, minus the buffer and cache rows that Linux-based network operating systems count as used).
  All values of a cycle come from one SNMP walk, so they share a timestamp. Nothing is lost to rounding.
- **Fallback: the stock "Network Generic Device by SNMP" template.** It stores rates, so the tool rebuilds counters
  from them; small error and discard rates are lost to integer rounding, and there is no CPU or memory.

If a host has both, the Forward template's items are used.

## What is verified

- The encoder follows Forward's `PerfProtos.proto`. Unit tests check hand-derived bytes, and a test decodes
  the output with `protoc --decode` against the real proto when `protoc` and a Forward source checkout
  (`FORWARD_SOURCE`) exist; otherwise that test is skipped.
- A payload built from a live lab device decoded cleanly with the real proto.
- The `VendorOS` table is generated from Forward's `VendorProtos.proto` (`internal/perf/gen`); regenerate it
  when Forward changes the enum.

## What is not done

- **Scheduled uploads are untested.** One manual `upload-perf --apply` of one lab device was accepted and shows up in
  Forward's interface views. `push-perf --apply` and its timer have not been run. Forward accepts uploads only from a
  collector registered in the network's organization, and mixing this with Forward's own SNMP collection on the same
  network would distort rates, which is what the existing-data check below guards against.
- CPU and memory come from HOST-RESOURCES-MIB, which not every device supports (devices without it simply send
  none). Values are sent as fractions 0..1, matching Forward's thresholds. Only a Linux-based Arista device has been
  checked, and the memory figure subtracts cache and buffers; other platforms' rows may need a different
  `{$FWD.PERF.MEM.MATCHES}`.
- The cadence of ticks follows the interface polling cycle; behaviour with irregular polling is untested.

## Uploading

```bash
export FWDZBX_COLLECTOR_TOKEN=collector-xxxx:password     # the registered collector's token; never printed
fwdzbx --network-id <id> upload-perf --device <name> --minutes 15           # dry run
fwdzbx --network-id <id> upload-perf --device <name> --minutes 15 --apply   # sends it
```

- The dry run builds the payload and makes read-only Forward calls: it reports how many CPU, memory and
  interface-utilization rows Forward already holds for the device in the last day. With `--apply` it refuses to
  upload if there are any (Forward does not record who supplied a sample, and an upload for the same device and
  time overwrites it); `--allow-existing` overrides.
- One attempt, no retry: the outcome of a failed write is unknown. The collector must belong to the network's
  organization (otherwise 403). A bad payload is a 400.
- The payload timestamp is the send time, because Forward measures collector clock lag against it.
- Forward keeps one row per device and time, and silently drops a sample older than the newest it holds for that device
  (the SDK maintainers' reading of the code; resending an identical payload is believed harmless, not tested). For
  scheduled use see `push-perf` below, which keeps a watermark.
- **Not yet run with `--apply` against Forward.** The Go SDK maintainers say Forward's own SNMP performance collection
  does not need to be enabled for an upload to be accepted; that is their reading of the code, not something this
  project has seen work yet.

## Which devices get polled: the ones Forward knows

Forward's inventory decides. `fwdzbx sync-inventory --performance --apply` (or `FWDZBX_PERFORMANCE=1`) creates a Zabbix
host with an SNMP interface for every Forward device and links `Forward Performance by SNMP` to it, so Zabbix polls
exactly the devices Forward models and a new Forward device is polled after the next sync. Without `--performance` the
performance template is not linked. Import the template first; set the SNMP community once as a Zabbix global macro
`{$SNMP_COMMUNITY}` (hosts reference the macro, never a value). Linking only adds; it never unlinks anything. Verified in a
dry run against the lab (hosts already linked by hand showed no change); a first-time link through sync has not been run.

## On a schedule: `push-perf`

`fwdzbx push-perf` is what runs every five minutes (Forward's own collector polls at that rate). One run sends every
Forward device's new ticks in a single upload.

```bash
fwdzbx --network-id <id> push-perf                 # dry run: what would be sent, no state written
fwdzbx --network-id <id> push-perf --apply         # sends, then records what was sent
```

- **State.** Forward silently drops any sample older than the newest it holds for a device, so the job records the
  newest tick sent per device (`$STATE_DIRECTORY/perf-state.json` under systemd, else
  `~/.local/state/fwdzbx/perf-state.json`; `--state FILE` or `FWDZBX_STATE_FILE` overrides) and sends only newer
  ticks, and only ticks at least 90 seconds old, because Zabbix commits one polling cycle's items in separate
  transactions and the newest cycle can be half written. The watermark advances only after Forward accepts the upload.
  A damaged state file is an error, never a silent reset. If the upload succeeds but the watermark cannot be written,
  the next run re-sends the same ticks (believed harmless, see above).
- **First time per device.** Before the first upload the tool writes a *claim* for the device into the state file, then
  uploads. A device that is not claimed is first checked against Forward's existing CPU, memory and utilization data
  and skipped if any exists, so the job does not start mixing with Forward's own polling or an unrelated upload. A
  claimed device is not checked again, so if another source starts supplying a claimed device later this tool will not
  notice. Losing the state file turns every device back into "unclaimed", which then reads our own earlier rows as
  another source's and skips them until you pass `--allow-existing`; keep the state file (or use `StateDirectory`).
  `upload-perf --apply` records a claim in the default state file too.
- **Only the Forward template's counters are sent.** The stock template stores rates, which would have to be rebuilt
  into counters from each run's window start, so the same tick would carry a different value from one run to the next.
  A device with only stock-template data is skipped with that reason.
- **Skipped devices** are listed with a reason: no Zabbix host, no new settled ticks, no Forward-template counters, an OS
  Forward does not know, or a read that failed (one device's failure does not stop the others).
- `--minutes N` (default 30) is how far back to read Zabbix, so a few missed runs are made up.
- Install: `deploy/systemd/fwdzbx-push-perf.{service,timer}` (every 5 minutes, `StateDirectory=fwdzbx`); put
  `FWDZBX_COLLECTOR_TOKEN` in the env file. **The Go timer has been verified with `systemd-analyze verify` only; it has
  not been run, and `push-perf --apply` has not been run against Forward.** Containers: mount a volume for the state directory (`-v fwdzbx-state:/state -e FWDZBX_STATE_FILE=/state/perf-state.json`) or every run starts with no state.
