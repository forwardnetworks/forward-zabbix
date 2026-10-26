"""Work shared by the CLI and the webhook receiver."""
from __future__ import annotations

from dataclasses import dataclass

from forward_sdk import ForwardClient

from . import plan as P
from .config import ZabbixSettings
from .forward import fetch_devices, newest_snapshot
from .zabbix import connect, ensure_network_host, existing_targets, push_values


@dataclass
class StatusResult:
    snapshot_id: str
    devices: int
    values: list[tuple[str, str, str]]
    skipped_hosts: list[str]
    processed: int = 0
    rejected: int = 0


def collect_status(network_id: str):
    with ForwardClient.from_env(network_id=network_id) as fwd:
        devices = fetch_devices(fwd, network_id)
        snapshot_id, epoch = newest_snapshot(fwd, network_id)
    return devices, snapshot_id, P.status_values(devices, network_id, snapshot_id, epoch)


def send_values(settings: ZabbixSettings, network_id: str, values: list[tuple[str, str, str]]):
    """Push what has an item in Zabbix. Returns (processed, rejected, skipped host names)."""
    with connect(settings) as zbx:
        ensure_network_host(zbx, network_id)
        have = existing_targets(zbx, values)
    sendable = [v for v in values if (v[0], v[1]) in have]
    skipped = sorted({h for h, k, _ in values if (h, k) not in have})
    processed, rejected = push_values(settings, sendable) if sendable else (0, 0)
    return processed, rejected, skipped


def push_status(settings: ZabbixSettings, network_id: str) -> StatusResult:
    devices, snapshot_id, values = collect_status(network_id)
    processed, rejected, skipped = send_values(settings, network_id, values)
    return StatusResult(snapshot_id, len(devices), values, skipped, processed, rejected)
