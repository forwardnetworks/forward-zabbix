from __future__ import annotations

from contextlib import contextmanager
from typing import Iterator

from zabbix_utils import ItemValue, Sender, ZabbixAPI

from .config import ZabbixSettings
from .plan import Desired, SyncPlan, ZbxHost

SNMP_DETAILS = {"version": 2, "bulk": 1, "community": "{$SNMP_COMMUNITY}"}


@contextmanager
def connect(settings: ZabbixSettings) -> Iterator[ZabbixAPI]:
    api = ZabbixAPI(url=settings.url)
    if settings.token:
        api.login(token=settings.token)
    else:
        api.login(user=settings.user, password=settings.password)
    try:
        yield api
    finally:
        try:
            api.logout()
        except Exception:  # token auth has no session to end
            pass


def list_hosts(api: ZabbixAPI) -> list[ZbxHost]:
    rows = api.host.get(output=["hostid", "host", "name"], selectTags=["tag", "value"],
                        selectInterfaces=["interfaceid", "ip", "main"], selectHostGroups=["name"], selectParentTemplates=["host"])
    hosts = []
    for r in rows:
        ifaces = sorted(r["interfaces"], key=lambda i: i["main"] != "1")
        hosts.append(ZbxHost(
            hostid=r["hostid"], host=r["host"], name=r["name"],
            ips=tuple(i["ip"] for i in ifaces if i["ip"]),
            groups=frozenset(g["name"] for g in r["hostgroups"]),
            tags=tuple((t["tag"], t["value"]) for t in r["tags"]),
            interfaceid=ifaces[0]["interfaceid"] if ifaces else None,
            templates=frozenset(x["host"] for x in r["parentTemplates"])))
    return hosts


def _group_ids(api: ZabbixAPI, names: set[str]) -> dict[str, str]:
    have = {g["name"]: g["groupid"] for g in api.hostgroup.get(output=["groupid", "name"], filter={"name": sorted(names)})}
    for name in sorted(names - have.keys()):
        have[name] = api.hostgroup.create(name=name)["groupids"][0]
    return have


def _template_ids(api: ZabbixAPI, names: set[str]) -> dict[str, str]:
    have = {x["host"]: x["templateid"] for x in api.template.get(output=["templateid", "host"], filter={"host": sorted(names)})}
    missing = names - have.keys()
    if missing:
        raise RuntimeError(f"Zabbix template(s) not found: {', '.join(sorted(missing))}; import zabbix/forward-templates.yaml")
    return have


def _iface(ip: str) -> dict:
    return {"type": 2, "main": 1, "useip": 1, "ip": ip, "dns": "", "port": "161", "details": SNMP_DETAILS}


def apply_plan(api: ZabbixAPI, plan: SyncPlan) -> dict[str, int]:
    """Creates and updates only. Stale and colliding hosts are never touched."""
    wanted = {g for d in plan.create for g in d.groups} | {g for _, d, _ in plan.update for g in d.groups}
    ids = _group_ids(api, wanted) if wanted else {}
    tpl = _template_ids(api, {x for d in plan.create for x in d.templates} | {x for _, d, _ in plan.update for x in d.templates})
    for d in plan.create:
        api.host.create(host=d.host, name=d.name, interfaces=[_iface(d.ip)],
                        groups=[{"groupid": ids[g]} for g in sorted(d.groups)],
                        templates=[{"templateid": tpl[n]} for n in sorted(d.templates)],
                        tags=[{"tag": k, "value": v} for k, v in d.tags])
    for host, d, _ in plan.update:
        params = dict(hostid=host.hostid, name=d.name,
                      groups=[{"groupid": ids[g]} for g in sorted(d.groups)],
                      tags=[{"tag": k, "value": v} for k, v in d.tags])
        # host.update replaces the template list, so send what is already linked plus what is missing
        linked = _template_ids(api, set(host.templates) | set(d.templates))
        params["templates"] = [{"templateid": linked[n]} for n in sorted(set(host.templates) | set(d.templates))]
        if d.ip not in host.ips and host.interfaceid:
            params["interfaces"] = [{**_iface(d.ip), "interfaceid": host.interfaceid}]
        api.host.update(**params)
    return {"created": len(plan.create), "updated": len(plan.update)}


def ensure_network_host(api: ZabbixAPI, network_id: str) -> str:
    """The host that carries network-level trapper items. Created once; a same-named host we do not own is an error."""
    from .plan import MANAGED_BY, NETWORK_TEMPLATE, ROOT_GROUP, network_host_name
    name = network_host_name(network_id)
    found = api.host.get(output=["hostid"], filter={"host": [name]}, selectTags=["tag", "value"])
    if found:
        if ("managed-by", MANAGED_BY) not in {(x["tag"], x["value"]) for x in found[0]["tags"]}:
            raise RuntimeError(f"host '{name}' exists and is not managed by fwdzbx; refusing to write to it")
        return name
    gid = _group_ids(api, {ROOT_GROUP})[ROOT_GROUP]
    tid = _template_ids(api, {NETWORK_TEMPLATE})[NETWORK_TEMPLATE]
    api.host.create(host=name, name=f"Forward network {network_id}", groups=[{"groupid": gid}],
                    templates=[{"templateid": tid}],
                    tags=[{"tag": "managed-by", "value": MANAGED_BY}, {"tag": "fwd-network", "value": network_id}])
    return name


def push_values(settings: ZabbixSettings, values: list[tuple[str, str, str]]) -> tuple[int, int]:
    """Send trapper values; returns (processed, failed). Failed usually means no such host/item in Zabbix."""
    from urllib.parse import urlparse
    import os
    server = os.environ.get("ZABBIX_SERVER") or urlparse(settings.url).hostname or "127.0.0.1"
    port = int(os.environ.get("ZABBIX_TRAPPER_PORT", "10051"))
    resp = Sender(server=server, port=port).send([ItemValue(h, k, v) for h, k, v in values])
    return resp.processed, resp.failed


def existing_targets(api: ZabbixAPI, values: list[tuple[str, str, str]]) -> set[tuple[str, str]]:
    """(host, key) pairs that have a trapper item in Zabbix. The trapper protocol only returns counts,
    so this is how a push can name what it skipped."""
    keys = sorted({k for _, k, _ in values})
    rows = api.item.get(output=["key_"], selectHosts=["host"], filter={"key_": keys}, templated=False)
    return {(h["host"], r["key_"]) for r in rows for h in r["hosts"]}


def open_problems(api: ZabbixAPI) -> list[tuple[str, str, str, bool]]:
    """(eventid, problem name, host, already has a Forward note) for open problems on fwdzbx-managed hosts."""
    from .plan import MANAGED_BY, NOTE_PREFIX
    rows = api.problem.get(output=["eventid", "name", "objectid"], selectAcknowledges=["message"], suppressed=False)
    if not rows:
        return []
    trig = api.trigger.get(output=["triggerid"], triggerids=[r["objectid"] for r in rows],
                           selectHosts=["host"], selectTags=["tag", "value"], monitored=True)
    host_of = {x["triggerid"]: x["hosts"][0]["host"] for x in trig if x["hosts"]}
    managed = {h["host"] for h in api.host.get(output=["host"], tags=[{"tag": "managed-by", "value": MANAGED_BY}])}
    return [(r["eventid"], r["name"], host_of[r["objectid"]],
             any(a["message"].startswith(NOTE_PREFIX) for a in r["acknowledges"]))
            for r in rows if host_of.get(r["objectid"]) in managed]


def add_note(api: ZabbixAPI, eventid: str, message: str) -> None:
    api.event.acknowledge(eventids=[eventid], action=4, message=message)  # 4 = add message only


def problem_counts(api: ZabbixAPI) -> dict[str, tuple[int, int]]:
    """Open, unsuppressed problems per monitored host (casefolded name): (count, worst severity 0-5)."""
    rows = api.problem.get(output=["objectid", "severity"], suppressed=False)
    if not rows:
        return {}
    trig = api.trigger.get(output=["triggerid"], triggerids=[r["objectid"] for r in rows],
                           selectHosts=["host"], monitored=True)
    host_of = {x["triggerid"]: x["hosts"][0]["host"].casefold() for x in trig if x["hosts"]}
    out: dict[str, tuple[int, int]] = {}
    for r in rows:
        host = host_of.get(r["objectid"])
        if host:
            n, worst = out.get(host, (0, 0))
            out[host] = (n + 1, max(worst, int(r["severity"])))
    return out
