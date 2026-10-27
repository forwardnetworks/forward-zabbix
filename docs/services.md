# Service tree

`fwdzbx sync-services` builds a Zabbix **service tree** that mirrors Forward's topology, so one view answers "is
anything wrong at this site, or behind this device?". Forward knows the layout; Zabbix works out the status.

```bash
fwdzbx --network-id <id> sync-services --vantage <device>            # dry run
fwdzbx --network-id <id> sync-services --vantage <device> --apply
```

`--vantage` is required and works as it does for [`sync-topology`](topology.md): the Forward device(s) Zabbix reaches the
network through. Run `sync-inventory --apply` first (see below).

## What it builds

- **One root** for the network, `Forward network <id>`.
- **For each device, a container service** named after the device, placed under the container of the device it sits
  behind (the same method as `sync-topology`; the two can differ, since this one needs the `fwd-device` host tag and that one the reachability trigger). The container holds the devices behind it.
- **Under each container, a leaf** named `<device> (device itself)`. It carries the problem tags that match that device's
  problems, so it shows the device's own state.

Zabbix does not allow a service to have both problem tags and children, which is why a device is two services.

**What a service's status means:** the worst problem on that device **or anything behind it**. It is a health roll-up,
not an impact view: a failure at a branch makes the services above it show that something beneath them is wrong. The
status is calculated by Zabbix ("most critical of child services").

## Rules

- **Prerequisite: the `fwd-device` host tag.** Problems carry their host's tags, and the leaf finds a device's problems by
  `fwd-device=<device name>`. `sync-inventory --apply` adds that tag (one update per existing host). A device whose host
  lacks it is skipped and listed, because a service for it would stay green while the device was down.
- **Only problems that open after the tag is on the host carry it.** A problem opened earlier is not matched, so the tree
  fills in as new problems arrive.
- **Ownership.** Services are found by their tags (`managed-by=forward-zabbix`, `fwd-network`, `fwd-service`,
  `fwd-device`), not by name, since Zabbix service names are not unique. Another network's services and ones a person made,
  even with the same name, are never matched or changed. An update replaces a service's parent list, so it sends the
  parent it should have plus any parents this tool does not own.
- **Never deletes.** A service whose device has gone is listed as stale and left in place. It stays under the root, and if
  its Zabbix host is still alive and raising problems, the root keeps showing them; remove the stale services (and host)
  yourself.
- **Hand edits are repaired.** A changed name, status rule or problem tag on a service this tool made is put back on the
  next `--apply`. Two services claiming the same device and kind (for example a cloned service, tags included) are left
  alone and listed as duplicates; delete one.
- **The leaf matches on `fwd-device` alone.** Any host, from this tool or not, whose problems carry that tag with the same
  value is counted, so device names must be unique across what you sync into one Zabbix.
- **Devices with no link path to the vantage** hang directly under the network root and are listed as unreachable.
- Running it twice changes nothing the second time. A refused service is listed under failures and the command exits 1;
  its children are skipped and reported.
