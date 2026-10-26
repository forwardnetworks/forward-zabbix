"""Fill a Zabbix with a made-up Forward inventory, using this project's own code. No Forward needed.

    export ZABBIX_URL=http://127.0.0.1:8082 ZABBIX_USER=Admin ZABBIX_PASSWORD=zabbix ZABBIX_SERVER=127.0.0.1 ZABBIX_TRAPPER_PORT=10052
    uv run python examples/synthetic_demo.py

It imports the templates, syncs 12 synthetic devices, pushes a collection result for each (one failed),
adds a hand-made host Forward does not know, then adds the Forward note to the resulting problem. Use a
throwaway Zabbix: it creates hosts and problems. The screenshots in docs/ were taken this way.
"""
from __future__ import annotations

import pathlib
import time

from fwdzbx import plan as P
from fwdzbx.config import ZabbixSettings
from fwdzbx.model import FwdDevice, Link
from fwdzbx.service import send_values
from fwdzbx.zabbix import add_note, apply_plan, connect, ensure_network_host, list_hosts, open_problems

NETWORK = "1001"
SNAPSHOT = ("48213", 1793046570)


def dev(name, site, vendor, model, os_, ip, collected="completed"):
    return FwdDevice(name=name, site=site, vendor=vendor, model=model, os=os_, os_version="1.0",
                     device_type="SWITCH", ips=(ip,), collected=collected)


DEVICES = [
    dev("dc1-core-01", "dc1", "ARISTA", "7280R3", "ARISTA_EOS", "10.10.0.1"),
    dev("dc1-core-02", "dc1", "ARISTA", "7280R3", "ARISTA_EOS", "10.10.0.2"),
    dev("dc1-fw-01", "dc1", "PALO_ALTO_NETWORKS", "PA-5450", "PAN_OS", "10.10.0.31",
        collected="collectionFailed:AUTHENTICATION_FAILED"),
    dev("wan-core-01", "wan", "ARISTA", "7280R3", "ARISTA_EOS", "10.20.0.1"),
    dev("wan-core-02", "wan", "CISCO", "ASR1001-X", "CISCO_IOS_XE", "10.20.0.2"),
    *[dev(f"br-{c}-rtr-01", f"br-{c}", "CISCO", "ISR4331", "CISCO_IOS_XE", f"10.30.{i}.1")
      for i, c in enumerate(["nyc", "bos", "chi", "dal", "sfo", "sea", "den"], start=1)],
]
LINKS = {"dc1-core-01": [Link("Ethernet1", "dc1-core-02", "Ethernet1"), Link("Ethernet2", "wan-core-01", "Ethernet10"),
                         Link("Ethernet3", "dc1-fw-01", "ethernet1/1")],
         "dc1-fw-01": [Link("ethernet1/1", "dc1-core-01", "Ethernet3"), Link("ethernet1/2", "dc1-core-02", "Ethernet3")]}


def main() -> None:
    settings = ZabbixSettings.from_env()
    tpl = pathlib.Path(__file__).resolve().parent.parent / "zabbix" / "forward-templates.yaml"
    with connect(settings) as zbx:
        rules = {k: {"createMissing": True, "updateExisting": True} for k in ("template_groups", "templates", "items", "triggers")}
        zbx.configuration.import_(format="yaml", source=tpl.read_text(), rules=rules)
        # two hand-made hosts so the coverage gap has something to find
        gid = zbx.hostgroup.get(output=["groupid"], filter={"name": ["Linux servers"]})[0]["groupid"]
        have = {h["host"] for h in zbx.host.get(output=["host"])}
        for name, ip in (("legacy-switch-01", "10.99.0.5"), ("dc1-core-02-old", "10.10.0.2")):
            if name not in have:
                zbx.host.create(host=name, groups=[{"groupid": gid}],
                                interfaces=[{"type": 1, "main": 1, "useip": 1, "ip": ip, "dns": "", "port": "10050"}])
        ensure_network_host(zbx, NETWORK)  # create it now so the server has loaded it before the first push
        plan = P.plan_sync(DEVICES, list_hosts(zbx), NETWORK)
        print("sync:", apply_plan(zbx, plan), "| collisions:", [n for n, _ in plan.collisions])
        gap = P.coverage_gap(DEVICES, list_hosts(zbx))
        print("gap: unmonitored", gap.unmonitored, "| unmodeled", gap.unmodeled, "| drift", [n for n, _ in gap.drift])
    time.sleep(65)  # let the server load the new hosts (config cache)
    values = P.status_values(DEVICES, NETWORK, *SNAPSHOT)
    print("push:", send_values(settings, NETWORK, values)[:2])
    time.sleep(20)  # let the trigger fire
    with connect(settings) as zbx:
        by_host = {P.technical_name(d.name).casefold(): d for d in DEVICES}
        for eventid, name, host, done in open_problems(zbx):
            d = by_host.get(host.casefold())
            if d and not done:
                add_note(zbx, eventid, P.enrichment_note(d, LINKS.get(d.name, []), SNAPSHOT[0]))
                print("note added to:", name)


if __name__ == "__main__":
    main()
