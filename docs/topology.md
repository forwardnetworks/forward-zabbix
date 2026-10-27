# Topology-aware alert reduction

When a core router goes down, every device behind it stops answering too, and Zabbix raises an alarm for each. The
real fault is one device. `fwdzbx sync-topology` uses Forward's links to tell Zabbix which device each one sits
behind, so the **"not answering SNMP" alarm** for a device behind a failed one stays quiet and the one that failed is the
alarm you see. It covers that one alarm per device, not every trigger a device can raise (see below).

It does this with Zabbix **trigger dependencies**: a trigger that depends on another does not raise a problem while
the other is in problem. Forward knows the layout. Zabbix applies it.

## Use

```bash
fwdzbx --network-id <id> sync-topology --vantage <device>            # dry run
fwdzbx --network-id <id> sync-topology --vantage <device> --apply
```

Run `sync-inventory --apply --performance` first, so the hosts exist and Zabbix polls them over SNMP, and import the
current `zabbix/forward-templates.yaml`, which adds the trigger this command works on (see below). Without something
polling the host over SNMP, that trigger never fires (see the end of this page).

**`--vantage` is required.** It names the Forward device Zabbix reaches the network through. "Behind" has no meaning
without a starting point. Give the option more than once if Zabbix has two ways in (`--vantage a --vantage b`), or set
`FWDZBX_VANTAGE=a,b`. A device that is behind both depends on neither.

## What it sets

- **One trigger per host**: `Forward: <host> is not answering SNMP`, from the `Forward Device` template. It reads
  Zabbix's own SNMP availability for the host, so it adds no polling. The dependency is set on this trigger only.
- **One dependency per device**: the device it sits behind. A device depends on the single device that every path
  from the vantage to it must cross. A device with two uplinks depends on the device above both, not on either uplink,
  because Zabbix suppresses a trigger when *any* of its dependencies is in problem. Picking one uplink would hide a
  real failure whenever the other one went down.
- **Hops over unmanaged devices**: a device behind something Zabbix does not monitor (no host, or a host this tool does
  not own) depends on the nearest monitored device above it. The dry run lists these.

## Ownership

- It touches only hosts tagged `managed-by=forward-zabbix` for this network, the same rule as `sync-inventory`. A host
  it does not own is reported under "Left alone" and not changed.
- A dependency is **this tool's** when it points at another managed host's reachability trigger. Those are replaced to
  match the topology, which also removes ones left by an earlier vantage or by a device that has gone. Every other
  dependency on the trigger (one on a trigger of the same host, anything a person added elsewhere) is kept.
- If you add a dependency by hand between two reachability triggers, the next `--apply` replaces it.
- It never deletes a host, a trigger or a template.
- `--apply` removes old dependencies first and adds new ones second, so moving the vantage, which turns edges around,
  does not ask Zabbix for a loop made of this tool's own dependencies. A loop through a dependency someone added by hand
  is refused by Zabbix: the host is listed under failures, the command exits 1 and the rest carry on. If an addition
  fails after the removal succeeded, that host is left depending only on what the tool does not own; run it again.
- Running it twice changes nothing the second time.

## What it does not cover

- **Other triggers.** Only this tool's own `not answering SNMP` trigger gets dependencies. The availability and ICMP
  triggers of a stock template such as `Network Generic Device by SNMP` are not changed, so if those fire on your hosts,
  devices behind a failure will still alarm through them. Whether they fire depends on how you set Zabbix up.
- **Interface alarms.** Zabbix discovers those per interface, and discovered triggers cannot be edited per host.
  Only the device-level reachability alarm is suppressed.
- **Out-of-band management.** The tool assumes Zabbix polls a device across the same links Forward models. If it
  polls a separate management network, Forward's data-plane links do not describe Zabbix's path, and the result is
  meaningless. Use a vantage on that network only if Forward models it.
- **Devices Forward has no links for.** A device Forward could not collect has no links, so it is listed as
  unreachable and gets no dependency.
- **Scale.** The calculation is O(n × (n + links)) in time and keeps a set per device, so memory can reach O(n²) in a
  deep topology. It was run on 26 devices; larger networks are untested.

## Template

`Forward Device` gains the internal item `zabbix[host,"snmp",available]` and the trigger on it. The key is spelled with
the type quoted because the stock `Network Generic Device by SNMP` template already defines the unquoted key and a host
cannot inherit the same key from two templates. Both mean the same thing. Importing the template again leaves the dependencies in place (relinking was not tested).

## What was measured

Two pairs of Zabbix hosts that do not answer SNMP, created together under the same conditions: in one pair the child
trigger depends on the parent's, in the other it does not. About eight minutes after creation:

| Pair | Parent trigger | Child trigger |
|---|---|---|
| Without a dependency | problem | **problem** |
| With the dependency | problem | **ok** (still ok after another 90 s) |

So a failure above a device raises the one alarm, not one per device. On the test network, applying the plan sets
dependencies on 22 of 24 monitored devices, a second run changes nothing, and they survive importing the template again.

Two limits seen in the same test:

- **Problems already open stay open.** Zabbix suppresses new problems on a dependent trigger; it does not close ones
  that were open when the dependency was added. Existing alarms clear when the device recovers. The first outage after
  `sync-topology --apply` is the first one that is reduced.
- **The trigger needs something to poll.** `zabbix[host,"snmp",available]` only changes when Zabbix polls an SNMP item
  on the host. `sync-inventory --performance` links a template that does. A stock SNMP template does too, but brings its
  own availability triggers that this tool does not touch (see above). A host with only the `Forward Device` template has
  nothing polling it and never raises this alarm.
