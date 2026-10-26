from __future__ import annotations

from forward_sdk import ForwardClient

from .model import FwdDevice, Link

# Field names verified against a live Forward (an unknown field is a runtime error).
DEVICES_QUERY = """
foreach d in network.devices
select {
  name: d.name,
  site: d.locationName,
  tags: d.tagNames,
  vendor: d.platform.vendor,
  model: d.platform.model,
  os: d.platform.os,
  osVersion: d.platform.osVersion,
  deviceType: d.platform.deviceType,
  ips: d.platform.managementIps,
  collected: d.snapshotInfo.result
}
"""


def fetch_devices(client: ForwardClient, network_id: str) -> list[FwdDevice]:
    result = client.nqe.query(DEVICES_QUERY, network_id=network_id)
    rows = getattr(result, "items", result)
    return sorted((_device(r) for r in rows), key=lambda d: d.name.casefold())


def _device(row: dict) -> FwdDevice:
    return FwdDevice(
        name=row["name"],
        site=row.get("site") or "",
        vendor=row.get("vendor") or "",
        model=row.get("model") or "",
        os=row.get("os") or "",
        os_version=row.get("osVersion") or "",
        device_type=row.get("deviceType") or "",
        ips=tuple(row.get("ips") or ()),
        tags=tuple(sorted(row.get("tags") or ())),
        collected=_collected(row.get("collected")),
    )


def _collected(value) -> str:
    """snapshotInfo.result is a union: a plain string, or {case, value} for a failure."""
    if isinstance(value, dict):
        return f"{value.get('case', '')}:{value.get('value', '')}".strip(":")
    return value or ""


def newest_snapshot(client: ForwardClient, network_id: str) -> tuple[str, int]:
    """(id, created epoch seconds) of the newest non-draft snapshot that has data. Chosen from the listing,
    not the SDK's latest_processed helper, which returned an older snapshot than a newer processed one."""
    from datetime import datetime
    listing = client.snapshots.list(network_id=network_id)
    usable = [s for s in getattr(listing, "items", listing)
              if not s.is_draft and getattr(s.state, "value", s.state) == "PROCESSED"]
    if not usable:
        raise RuntimeError(f"network {network_id} has no processed snapshot")
    newest = max(usable, key=lambda s: s.created_at)
    created = newest.created_at
    epoch = int((created if isinstance(created, datetime) else datetime.fromisoformat(str(created).replace("Z", "+00:00"))).timestamp())
    return str(newest.id), epoch


LINKS_QUERY = """
foreach d in network.devices
foreach i in d.interfaces
foreach l in i.links
select {device: d.name, iface: i.name, peer: l.deviceName, peerIface: l.ifaceName}
"""


def fetch_links(client: ForwardClient, network_id: str) -> dict[str, list[Link]]:
    result = client.nqe.query(LINKS_QUERY, network_id=network_id)
    out: dict[str, list[Link]] = {}
    for r in getattr(result, "items", result):
        out.setdefault(r["device"], []).append(Link(r["iface"], r["peer"], r["peerIface"]))
    return out
