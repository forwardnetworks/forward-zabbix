# Performance data (experimental)

Idea: let Zabbix be the SNMP poller and feed its history to Forward's performance views, instead of running a
second SNMP collection.

`fwdzbx export-perf` builds the payload and **uploads nothing**; `fwdzbx upload-perf` does the same and, only with `--apply`, sends it to Forward as the registered collector:

```bash
fwdzbx --network-id <id> export-perf --device <forward device name> --minutes 180 -o perf.pb
```

It reads the device's Zabbix SNMP history (the stock "Network Generic Device by SNMP" template), rebuilds
cumulative interface counters from Zabbix's stored rates (Forward diffs counters itself), and writes a
`PerfDataPB`.

## What is verified

- The encoder follows Forward's `PerfProtos.proto`. Unit tests check hand-derived bytes, and a test decodes
  the output with `protoc --decode` against the real proto when `protoc` and a Forward source checkout
  (`FORWARD_SOURCE`) exist; otherwise that test is skipped.
- A payload built from a live lab device decoded cleanly with the real proto.
- The `VendorOS` table is generated from Forward's `VendorProtos.proto` (`internal/perf/gen`); regenerate it
  when Forward changes the enum.

## What is not done

- **No upload has been made.** Forward accepts uploads only from a collector registered in the network's
  organization, and mixing this with Forward's own SNMP collection on the same network would distort rates.
  Check that first, and get approval, before any upload.
- Zabbix stores rates, not counters. Reconstruction is lossy for very small error and discard rates, because
  integer items truncate them. A dedicated raw-counter template would be lossless.
- CPU and memory appear only if the host has `system.cpu.util` / `vm.memory.util` items; the stock generic
  template has none. Values are sent as fractions 0..1, matching Forward's thresholds.
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
- Repeated uploads of overlapping windows rewrite the same rows (per the SDK maintainers, untested). There is no
  scheduler or watermark yet, so this is a manual PoC step.
- **Not yet run with `--apply` against Forward.** Forward-side performance collection settings for the network may
  also need to be enabled for the views to show the data; not checked.
