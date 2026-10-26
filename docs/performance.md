# Performance data (experimental)

Idea: let Zabbix be the SNMP poller and feed its history to Forward's performance views, instead of running a
second SNMP collection.

`fwdzbx export-perf` builds the payload and **uploads nothing**:

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
