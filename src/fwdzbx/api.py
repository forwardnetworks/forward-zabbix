"""Public Python API. The CLI is a thin layer over these functions; pipelines should call them directly.

Every function reads Forward and Zabbix credentials from the environment unless you pass them in
(``zabbix=ZabbixSettings(...)``, ``forward=ForwardClient(...)``). Functions that can change something take
``apply`` and do nothing but read when it is false.
"""
from __future__ import annotations

from dataclasses import asdict, dataclass, field, is_dataclass
from typing import Any

from forward_sdk import ForwardClient

from . import plan as P
from .config import ZabbixSettings
from .forward import fetch_devices, fetch_links, newest_snapshot
from .model import FwdDevice
from .service import collect_status, send_values
from .zabbix import add_note, apply_plan, connect, list_hosts, open_problems, problem_counts

__all__ = ["devices", "coverage_gap", "sync_inventory", "push_status", "enrich", "export_status", "to_jsonable",
           "InventorySync", "StatusPush", "Note"]


@dataclass
class InventorySync:
    plan: P.SyncPlan
    applied: dict[str, int] | None = None  # None on a dry run


@dataclass
class StatusPush:
    snapshot_id: str
    devices: int
    values: list[tuple[str, str, str]]
    collection_problems: list[tuple[str, str]]  # (device, result)
    pushed: bool = False
    processed: int = 0
    rejected: int = 0
    skipped_hosts: list[str] = field(default_factory=list)


@dataclass
class Note:
    eventid: str
    problem: str
    text: str
    added: bool = False


def _forward(network_id: str, forward: ForwardClient | None):
    return forward if forward is not None else ForwardClient.from_env(network_id=network_id)


def devices(network_id: str, *, forward: ForwardClient | None = None) -> list[FwdDevice]:
    """Devices Forward holds for a network. Read-only."""
    client = _forward(network_id, forward)
    try:
        return fetch_devices(client, network_id)
    finally:
        if forward is None:
            client.close()


def coverage_gap(network_id: str, *, zabbix: ZabbixSettings | None = None,
                 forward: ForwardClient | None = None) -> P.Gap:
    """Diff Forward's inventory against every Zabbix host. Read-only."""
    inventory = devices(network_id, forward=forward)
    with connect(zabbix or ZabbixSettings.from_env()) as zbx:
        return P.coverage_gap(inventory, list_hosts(zbx))


def sync_inventory(network_id: str, *, apply: bool = False, zabbix: ZabbixSettings | None = None,
                   forward: ForwardClient | None = None) -> InventorySync:
    """Plan (and with apply=True, make) the Zabbix host changes. Creates and updates only; never deletes."""
    inventory = devices(network_id, forward=forward)
    with connect(zabbix or ZabbixSettings.from_env()) as zbx:
        plan = P.plan_sync(inventory, list_hosts(zbx), network_id)
        return InventorySync(plan, apply_plan(zbx, plan) if apply else None)


def push_status(network_id: str, *, apply: bool = False, zabbix: ZabbixSettings | None = None) -> StatusPush:
    """Collect per-device collection results and the newest snapshot; with apply=True push them to Zabbix."""
    inventory, snapshot_id, values = collect_status(network_id)
    problems = [(h, v) for h, k, v in values if k == "fwd.collection.result" and v != "completed"]
    result = StatusPush(snapshot_id, len(inventory), values, problems)
    if apply:
        result.processed, result.rejected, result.skipped_hosts = send_values(
            zabbix or ZabbixSettings.from_env(), network_id, values)
        result.pushed = True
    return result


def export_status(network_id: str, *, zabbix: ZabbixSettings | None = None,
                  forward: ForwardClient | None = None) -> list[dict[str, str]]:
    """One row per Forward device with what Zabbix says about it (see `plan.STATUS_COLUMNS`). Read-only.

    Feed it to Forward as a data file so NQE can join on `device`; this function does not write to Forward.
    """
    inventory = devices(network_id, forward=forward)
    with connect(zabbix or ZabbixSettings.from_env()) as zbx:
        return P.status_rows(inventory, list_hosts(zbx), problem_counts(zbx))


def enrich(network_id: str, *, apply: bool = False, zabbix: ZabbixSettings | None = None,
           forward: ForwardClient | None = None) -> list[Note]:
    """Notes for open Zabbix problems on managed hosts that do not have one yet; with apply=True, add them."""
    client = _forward(network_id, forward)
    try:
        by_host = {P.technical_name(d.name).casefold(): d for d in fetch_devices(client, network_id)}
        links = fetch_links(client, network_id)
        snapshot_id, _ = newest_snapshot(client, network_id)
    finally:
        if forward is None:
            client.close()
    notes: list[Note] = []
    with connect(zabbix or ZabbixSettings.from_env()) as zbx:
        for eventid, name, host, done in open_problems(zbx):
            device = by_host.get(host.casefold())
            if done or device is None:
                continue
            note = Note(eventid, name, P.enrichment_note(device, links.get(device.name, []), snapshot_id))
            if apply:
                add_note(zbx, eventid, note.text)
                note.added = True
            notes.append(note)
    return notes


def to_jsonable(obj: Any) -> Any:
    """Plain dicts, lists and strings for any result above, so a pipeline can json.dumps it."""
    if is_dataclass(obj) and not isinstance(obj, type):
        return {k: to_jsonable(v) for k, v in asdict(obj).items()}
    if isinstance(obj, (set, frozenset)):
        return sorted(to_jsonable(x) for x in obj)
    if isinstance(obj, dict):
        return {str(k): to_jsonable(v) for k, v in obj.items()}
    if isinstance(obj, (list, tuple)):
        return [to_jsonable(x) for x in obj]
    return obj
