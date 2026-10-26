# Validation

What was checked, against what, and what was not. Version under test: Zabbix 7.0.32, `zabbix-utils` 2.0.4.

## Automated

`uv run pytest`: unit tests for the planning and gap logic (synthetic devices), the receiver (an in-process
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
- `examples/pipeline_gate.py` and the `--json` / `--fail-on-gap` forms were run.
- The screenshots were produced from a clean Zabbix by `examples/synthetic_demo.py` with made-up devices.

## Not verified

- A real Forward **check-failure** webhook (`INTENT_VERIFICATION_FAILURE`, `NQE_VERIFICATION_FAILURE`). The
  receiver's handling is unit-tested from the documented payload shape only.
- Check pass/fail counts as Zabbix metrics (not built).
- The system-level systemd units and timers, and `DynamicUser`; only the sandbox properties were run.
- A Zabbix user with less than Super admin; Zabbix versions other than 7.0.
- Frameworks such as Airflow; only plain Python and shell use are shown.
- Forward UI screenshots (none are shipped; see [Forward setup](forward-setup.md#screenshots)).
