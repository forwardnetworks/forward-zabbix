"""Pure logic: inventory sync planning and the coverage-gap diff. No I/O."""
from __future__ import annotations

import re
from dataclasses import dataclass, field

from .model import FwdDevice, Link

MANAGED_BY = "forward-zabbix"
ROOT_GROUP = "Forward"
DEVICE_TEMPLATE = "Forward Device"
NETWORK_TEMPLATE = "Forward Network"
NETWORK_HOST_PREFIX = "forward-network-"
_BAD = re.compile(r"[^0-9A-Za-z_. -]")


def technical_name(name: str) -> str:
    """Zabbix technical host names allow a limited character set."""
    return _BAD.sub("_", name)


@dataclass(frozen=True)
class ZbxHost:
    hostid: str
    host: str
    name: str
    ips: tuple[str, ...]
    groups: frozenset[str]
    tags: tuple[tuple[str, str], ...]
    interfaceid: str | None = None
    templates: frozenset[str] = frozenset()

    @property
    def tagmap(self) -> dict[str, str]:
        return dict(self.tags)

    @property
    def managed(self) -> bool:
        return self.tagmap.get("managed-by") == MANAGED_BY

    @property
    def is_network_host(self) -> bool:
        """The host this tool creates to carry network-level values; it is bookkeeping, not a device."""
        return self.managed and self.host.startswith(NETWORK_HOST_PREFIX)


@dataclass(frozen=True)
class Desired:
    host: str
    name: str
    ip: str
    groups: frozenset[str]
    tags: tuple[tuple[str, str], ...]
    templates: frozenset[str] = frozenset({DEVICE_TEMPLATE})


def desired_host(device: FwdDevice, network_id: str) -> Desired | None:
    if not device.ips:
        return None
    groups = {ROOT_GROUP}
    if device.site:
        groups.add(f"{ROOT_GROUP}/Sites/{device.site}")
    if device.vendor:
        groups.add(f"{ROOT_GROUP}/Vendors/{device.vendor}")
    tags = [("managed-by", MANAGED_BY), ("fwd-network", network_id)]
    for key, value in (("fwd-site", device.site), ("fwd-vendor", device.vendor),
                       ("fwd-model", device.model), ("fwd-os", device.os),
                       ("fwd-type", device.device_type)):
        if value:
            tags.append((key, value))
    tags += [("fwd-tag", t) for t in device.tags]
    return Desired(technical_name(device.name), device.name, device.ips[0],
                   frozenset(groups), tuple(sorted(tags)))


@dataclass
class SyncPlan:
    create: list[Desired] = field(default_factory=list)
    update: list[tuple[ZbxHost, Desired, list[str]]] = field(default_factory=list)
    unchanged: list[str] = field(default_factory=list)
    collisions: list[tuple[str, str]] = field(default_factory=list)  # (device, reason)
    stale: list[str] = field(default_factory=list)  # managed hosts with no device; never deleted
    skipped_no_ip: list[str] = field(default_factory=list)


def plan_sync(devices: list[FwdDevice], hosts: list[ZbxHost], network_id: str) -> SyncPlan:
    plan = SyncPlan()
    by_host = {h.host.casefold(): h for h in hosts}
    by_ip: dict[str, ZbxHost] = {}
    for h in hosts:
        for ip in h.ips:
            by_ip.setdefault(ip, h)
    wanted: set[str] = set()
    for device in devices:
        want = desired_host(device, network_id)
        if want is None:
            plan.skipped_no_ip.append(device.name)
            continue
        wanted.add(want.host.casefold())
        existing = by_host.get(want.host.casefold())
        if existing is not None:
            if not existing.managed or existing.tagmap.get("fwd-network") != network_id:
                plan.collisions.append((device.name, f"host '{existing.host}' exists and is not managed by this sync"))
                continue
            changes = _diff(existing, want)
            (plan.update.append((existing, want, changes)) if changes else plan.unchanged.append(device.name))
            continue
        owner = by_ip.get(want.ip)
        if owner is not None:
            plan.collisions.append((device.name, f"{want.ip} is already monitored as '{owner.host}'"))
            continue
        plan.create.append(want)
    plan.stale = sorted(h.host for h in hosts
                        if h.managed and not h.is_network_host and h.tagmap.get("fwd-network") == network_id
                        and h.host.casefold() not in wanted)
    return plan


def _diff(existing: ZbxHost, want: Desired) -> list[str]:
    changes = []
    if want.ip not in existing.ips:
        changes.append(f"ip {','.join(existing.ips) or '-'} -> {want.ip}")
    if existing.name != want.name:
        changes.append("visible name")
    if existing.groups != want.groups:
        changes.append("groups")
    if tuple(sorted(existing.tags)) != want.tags:
        changes.append("tags")
    if not want.templates <= existing.templates:  # only ever adds; never unlinks a template someone else linked
        changes.append("templates")
    return changes


@dataclass
class Gap:
    unmonitored: list[str] = field(default_factory=list)  # in Forward, no Zabbix host
    unmodeled: list[str] = field(default_factory=list)  # in Zabbix, not in Forward
    drift: list[tuple[str, str]] = field(default_factory=list)  # (device, what differs)
    matched: int = 0


def coverage_gap(devices: list[FwdDevice], hosts: list[ZbxHost],
                 ignore_groups: frozenset[str] = frozenset({"Zabbix servers"})) -> Gap:
    candidates = [h for h in hosts if not (h.groups & ignore_groups) and not h.is_network_host]
    by_name: dict[str, ZbxHost] = {}
    for h in candidates:
        by_name.setdefault(h.host.casefold(), h)
        by_name.setdefault(h.name.casefold(), h)
    by_ip: dict[str, ZbxHost] = {}
    for h in candidates:
        for ip in h.ips:
            by_ip.setdefault(ip, h)
    gap, used = Gap(), set()
    for d in devices:
        host = by_name.get(d.name.casefold()) or by_name.get(technical_name(d.name).casefold())
        by = "name"
        if host is None:
            host = next((by_ip[ip] for ip in d.ips if ip in by_ip), None)
            by = "ip"
        if host is None:
            gap.unmonitored.append(d.name)
            continue
        used.add(host.hostid)
        gap.matched += 1
        if by == "ip":
            gap.drift.append((d.name, f"monitored under a different name '{host.host}'"))
        elif d.ips and not set(d.ips) & set(host.ips):
            gap.drift.append((d.name, f"IP differs: Forward {','.join(d.ips)} vs Zabbix {','.join(host.ips) or '-'}"))
        elif host.managed and host.tagmap.get("fwd-site", d.site) != d.site:
            gap.drift.append((d.name, f"site differs: Forward '{d.site}' vs Zabbix '{host.tagmap.get('fwd-site')}'"))
    gap.unmodeled = sorted(h.host for h in candidates if h.hostid not in used)
    return gap


def network_host_name(network_id: str) -> str:
    return f"{NETWORK_HOST_PREFIX}{network_id}"


def status_values(devices: list[FwdDevice], network_id: str, snapshot_id: str, snapshot_epoch: int
                  ) -> list[tuple[str, str, str]]:
    """(host, key, value) rows to push. A device without a collection result is skipped, not reported healthy."""
    net = network_host_name(network_id)
    failed = [d for d in devices if d.collected and d.collected != "completed"]
    rows = [(net, "fwd.devices.total", str(len(devices))),
            (net, "fwd.devices.collection_failed", str(len(failed))),
            (net, "fwd.snapshot.id", snapshot_id),
            (net, "fwd.snapshot.time", str(snapshot_epoch))]
    rows += [(technical_name(d.name), "fwd.collection.result", d.collected) for d in devices if d.collected]
    return rows


NOTE_PREFIX = "[Forward]"
_NOTE_LIMIT = 1900  # Zabbix acknowledgement messages are capped at 2048 characters


def enrichment_note(device: FwdDevice, links: list[Link], snapshot_id: str) -> str:
    """The text added to a Zabbix problem. Facts only: what Forward holds for the device in that snapshot."""
    lines = [f"{NOTE_PREFIX} {device.name} (snapshot {snapshot_id})",
             f"{device.vendor} {device.model} {device.os} {device.os_version}".strip()
             + (f", site {device.site}" if device.site else "")
             + (f", mgmt {', '.join(device.ips)}" if device.ips else "")]
    if device.collected and device.collected != "completed":
        lines.append(f"Forward could not collect this device ({device.collected}); its config and state in Forward are missing or stale.")
    elif device.collected == "completed":
        lines.append("Forward collected this device successfully in that snapshot.")
    if links:
        peers = sorted({link.peer for link in links}, key=str.casefold)
        lines.append(f"Directly connected to {len(peers)} device(s) over {len(links)} link(s):")
        by_peer: dict[str, list[Link]] = {}
        for link in links:
            by_peer.setdefault(link.peer, []).append(link)
        for peer in peers:
            ports = ", ".join(f"{l.iface}<->{l.peer_iface}" for l in sorted(by_peer[peer], key=lambda l: l.iface))
            lines.append(f"  {peer}: {ports}")
    else:
        lines.append("Forward models no links for this device.")
    text = "\n".join(lines)
    return text if len(text) <= _NOTE_LIMIT else text[:_NOTE_LIMIT - 20].rstrip() + "\n... (truncated)"


SEVERITIES = ("not classified", "information", "warning", "average", "high", "disaster")
STATUS_COLUMNS = ("device", "monitored", "zabbix_host", "open_problems", "worst_severity")


def status_rows(devices: list[FwdDevice], hosts: list[ZbxHost],
                problems: dict[str, tuple[int, int]]) -> list[dict[str, str]]:
    """One row per Forward device describing what Zabbix says about it: the data NQE can join on `device`.

    `problems` maps a Zabbix host name (casefolded) to (open problem count, worst severity 0-5).
    A device is matched to a host by technical name, else by IP, as in `coverage_gap`.
    """
    candidates = [h for h in hosts if not h.is_network_host]
    by_name = {h.host.casefold(): h for h in candidates}
    by_ip: dict[str, ZbxHost] = {}
    for h in candidates:
        for ip in h.ips:
            by_ip.setdefault(ip, h)
    rows = []
    for d in sorted(devices, key=lambda x: x.name):
        host = by_name.get(technical_name(d.name).casefold()) or next((by_ip[i] for i in d.ips if i in by_ip), None)
        count, worst = problems.get(host.host.casefold(), (0, 0)) if host else (0, 0)
        rows.append({"device": d.name, "monitored": "yes" if host else "no",
                     "zabbix_host": host.host if host else "", "open_problems": str(count),
                     "worst_severity": SEVERITIES[worst] if count else ""})
    return rows
