from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class FwdDevice:
    name: str
    site: str
    vendor: str
    model: str
    os: str
    os_version: str
    device_type: str
    ips: tuple[str, ...]
    tags: tuple[str, ...] = field(default_factory=tuple)
    collected: str = ""


@dataclass(frozen=True)
class Link:
    iface: str
    peer: str
    peer_iface: str
