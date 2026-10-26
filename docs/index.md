# forward-zabbix

Field integration between [Forward Networks](https://www.forwardnetworks.com/) and
[Zabbix](https://www.zabbix.com/). Experimental; not a Forward product; no SLA.

Forward is the source of truth for what the network **is**: devices, topology, configuration, policy.
Zabbix is the source of truth for what it is **doing now**: liveness, counters, alarms. This project moves
each side's knowledge to the other. Forward is not monitored by Zabbix; it is the system of record that
Zabbix is populated and enriched from.

## Why use it

**What Forward gives Zabbix**

- *Zabbix populated without hand-building it.* Hosts, groups and tags come from Forward's inventory and stay in
  step. Tags (site, vendor, model, OS) ride along on problems, so routing and filtering need no extra work.
- *Forward's blind spots show up where you already look.* A device Forward cannot log in to, a snapshot that
  stopped arriving, or a failing check becomes a Zabbix problem, in the same pane as everything else.
- *Context on a problem.* An open problem gets a note with Forward's view of the device: what it is, whether
  Forward collected it, and what it connects to.
- *A coverage report neither tool can produce alone.* Devices Forward models that Zabbix does not monitor,
  Zabbix hosts Forward has never seen, and matches that disagree on name or IP.

**What Zabbix gives Forward**

This project never changes Forward on its own. Zabbix's knowledge reaches Forward in one way so far:
`fwdzbx export-status` writes a CSV of what Zabbix says about each device, which you can upload to Forward as
a data file for NQE to join (an optional step you run yourself; see
[Forward setup](forward-setup.md#zabbix-data-in-nqe)). Other flows (a collection triggered by a Zabbix
event, before and after snapshots around a maintenance window, onboarding candidates) are designed in
[DESIGN.md](../DESIGN.md) and not built.

## What it does

| Direction | Flow | Command / component |
|---|---|---|
| Forward to Zabbix | Forward devices become Zabbix hosts, grouped and tagged by site, vendor, OS and type | `fwdzbx sync-inventory` |
| Forward to Zabbix | Per-device collection result and newest snapshot become trapper items; Zabbix raises problems when Forward cannot collect a device, or when no new snapshot arrives | `fwdzbx push-status`, or the webhook receiver |
| Forward to Zabbix | Failing intent and NQE checks reported by Forward's webhook become Zabbix problems | `fwdzbx serve` |
| Both | Which devices Forward models that Zabbix does not monitor, which Zabbix hosts Forward does not model, and which match but differ | `fwdzbx coverage-gap` |
| Zabbix to Forward | Open Zabbix problems on managed hosts get an acknowledgement note with Forward's view of the device: identity, whether it was collected, and its neighbors with ports | `fwdzbx enrich` |

## What it does not do

- It does not change Forward. It reads Forward (NQE and the snapshot list) and writes only to Zabbix.
- It does not poll devices. The items it creates are trapper items; hosts get an SNMP interface so you
  can attach your own polling templates, but no polling template is applied.
- It never deletes a Zabbix host. A managed host whose device has disappeared from Forward is reported as
  stale and left alone.
- It never modifies a Zabbix host it did not create (see [ownership](usage.md#ownership-rules)).

## Documentation

| Page | Read it to |
|---|---|
| [Install](install.md) | Get the tool, and the Forward and Zabbix credentials it needs |
| [Configuration](configuration.md) | Look up every environment variable |
| [Zabbix setup](zabbix-setup.md) | Import the templates and understand what appears in Zabbix |
| [Forward setup](forward-setup.md) | Create the webhook and find the network id |
| [Usage](usage.md) | Run each command, read its output and exit codes |
| [Run as a service](service.md) | systemd units and timers, or the container image |
| [Go API](go-api.md) | Call it from your own pipelines, or consume `--json` |
| [Architecture](architecture.md) | See how the pieces fit and why |
| [Troubleshooting](troubleshooting.md) | Fix the usual failures |
| [Validation](validation.md) | See what was tested, against what, and what was not |
| [Design notes](../DESIGN.md) | Read the original design, including flows not yet built |

Screenshots in these pages use **synthetic data**: a clean Zabbix filled by this project's own code from a
made-up inventory, so no customer network appears.
