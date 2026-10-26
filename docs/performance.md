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

- **No upload has been made.** Forward accepts uploads only from a collector registered in the network's
  organization, and mixing this with Forward's own SNMP collection on the same network would distort rates.
  Check that first, and get approval, before any upload.
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
- Repeated uploads of overlapping windows rewrite the same rows (per the SDK maintainers, untested). There is no
  scheduler or watermark yet, so this is a manual PoC step.
- **Not yet run with `--apply` against Forward.** The Go SDK maintainers say Forward's own SNMP performance collection
  does not need to be enabled for an upload to be accepted; that is their reading of the code, not something this
  project has seen work yet.
