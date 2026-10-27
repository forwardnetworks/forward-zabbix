# Validation

What was checked, against what, and what was not. Version under test: Zabbix 7.0.32. The tool was first written in Python and ported to Go; the Go port was checked against the Python original (below), and the Python original is no longer in the tree.

## Automated

`go vet ./... && go test -race ./...`: unit tests for the planning and gap logic (synthetic devices), the receiver (an in-process
HTTP server: auth, routing, ignored networks, de-duplication, size limits), and the API and CLI contract
(`--json` output, exit codes). They need neither Forward nor Zabbix.

## Run for real

Against a hosted Forward network of 26 devices, collected by a local collector, and a throwaway Zabbix 7.0:

- Inventory sync created 24 hosts (two were collisions with seeded hosts, left alone); a second run changed
  nothing. Coverage gap found the seeded drift and the unmodeled host exactly.
- A device Forward genuinely could not log in to raised a Zabbix problem, got an enrichment note, and the
  freshness problem cleared when a new snapshot arrived.
- A real `SNAPSHOT_READY` webhook from Forward, through a tunnel, reached the receiver authenticated, and
  Zabbix then held the new snapshot's id and time.
- The receiver ran under systemd user-service sandboxing and as a hardened container; both answered 401
  without credentials and pushed status for an event.
- Go port parity: on the same live Forward network and Zabbix, the Go and Python `--json` output of `devices`, `coverage-gap`, `sync-inventory` and `push-status`, and the `export-status` CSV, were identical. The receiver answered 401, 404, 400, 413, 202 and 200 (ignored and duplicate) as specified, and a real event pushed status to Zabbix. `examples/pipeline_gate` and `--fail-on-gap` have not been re-run since the port.
- `export-status` output was uploaded to Forward as a data file, attached to one network, and after the next snapshot an NQE query joining it to `network.devices` returned all 26 devices with their Zabbix values.
- The screenshots were produced from a clean Zabbix by the earlier Python `synthetic_demo` with made-up devices; `examples/synthetic_demo` is its Go port and has not been re-run.

## Topology dependencies

`sync-topology` was run against a live Forward network and a local Zabbix 7.0: the plan matched the network's shape,
`--apply` set 22 dependencies, a second run changed nothing and a template re-import kept them. A controlled test with
two pairs of scratch hosts, one pair with a dependency and one without, showed the dependent child staying quiet while its parent was in
problem (see [Topology](topology.md#what-was-measured)). The dependency algorithm is unit-tested on chains, diamonds,
rings, parallel links, disconnected devices, unmanaged parents and two vantages.

## Change evidence

`change-evidence` was run against a live Forward network and a local Zabbix 7.0, read-only: with two snapshot ids, and
with a temporary Zabbix maintenance (which resolved the window, picked the snapshots on either side, and turned its host
into the expected device). It reported a device that was expected to change and did not, the check results that moved,
and a problem that opened and recovered inside the window; `--fail-on-diff` exited 1. The first version read only
problems still open and missed the recovered one, found by comparing with a direct Zabbix query; it now reads events.
The input rules, snapshot selection, the expectation judgement, the parsing of Forward's changed-device list and the Zabbix reads (maintenance, with host groups; events in the window) are unit-tested against fakes.

## Not verified

- A real Forward **check-failure** webhook (`INTENT_VERIFICATION_FAILURE`, `NQE_VERIFICATION_FAILURE`). The
  receiver's handling is unit-tested from the documented payload shape only.
- Check pass/fail counts as Zabbix metrics (not built).
- The container image and systemd sandbox results above were measured on the Python build; the Go image was built and started (`--help`) but not run as a receiver, and the Go units are not yet re-run.
- The system-level systemd units and timers, and `DynamicUser`; only the sandbox properties were run.
- `change-evidence` where devices really changed configuration: the test pair showed none, so the changed-device list was checked against a fake response and not against live changes.
- `sync-topology` on networks larger than the 26-device test network, and a vantage move or a refused dependency against a real Zabbix (both are covered by unit tests with a fake Zabbix only).
- A Zabbix user with less than Super admin; Zabbix versions other than 7.0.
- Frameworks such as Airflow; only plain Go and shell use are shown.
- Forward UI screenshots (none are shipped; see [Forward setup](forward-setup.md#screenshots)).
