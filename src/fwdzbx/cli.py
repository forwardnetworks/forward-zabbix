from __future__ import annotations

import argparse
import json
import logging
import os
import sys

from . import api
from . import plan as P
from . import service
from .config import ConfigError, ZabbixSettings, forward_network_id


def _emit(args, payload) -> None:
    print(json.dumps(api.to_jsonable(payload), indent=2, sort_keys=True))


def _section(title: str, items: list[str]) -> None:
    if items:
        print(f"\n{title} ({len(items)}):")
        for i in items:
            print(f"  {i}")


def cmd_devices(args) -> int:
    devices = api.devices(forward_network_id(args.network_id))
    if args.json:
        _emit(args, devices)
        return 0
    for d in devices:
        print(f"{d.name:28} {d.site or '-':14} {d.vendor:10} {d.model:12} {','.join(d.ips) or '-':16} {d.collected}")
    print(f"\n{len(devices)} devices")
    return 0


def cmd_coverage_gap(args) -> int:
    network_id = forward_network_id(args.network_id)
    gap = api.coverage_gap(network_id)
    clean = not (gap.unmonitored or gap.unmodeled or gap.drift)
    if args.json:
        _emit(args, gap)
    else:
        print(f"matched in Zabbix: {gap.matched}")
        _section("In Forward, not monitored in Zabbix", gap.unmonitored)
        _section("In Zabbix, not modeled in Forward", gap.unmodeled)
        _section("In both, but different", [f"{n}: {why}" for n, why in gap.drift])
    return 0 if clean or not args.fail_on_gap else 1


def cmd_sync(args) -> int:
    result = api.sync_inventory(forward_network_id(args.network_id), apply=args.apply)
    if args.json:
        _emit(args, result)
        return 0
    plan = result.plan
    print(f"create {len(plan.create)}  update {len(plan.update)}  unchanged {len(plan.unchanged)}  "
          f"collisions {len(plan.collisions)}  stale {len(plan.stale)}  no-ip {len(plan.skipped_no_ip)}")
    _section("Would create", [d.host for d in plan.create])
    _section("Would update", [f"{h.host}: {', '.join(c)}" for h, _, c in plan.update])
    _section("Collisions (left untouched)", [f"{n}: {why}" for n, why in plan.collisions])
    _section("Stale: managed hosts with no Forward device (never deleted)", plan.stale)
    _section("Skipped, no management IP", plan.skipped_no_ip)
    print("\napplied:" if args.apply else "\ndry run; pass --apply to make these changes", result.applied or "")
    return 0


def cmd_push_status(args) -> int:
    result = api.push_status(forward_network_id(args.network_id), apply=args.apply)
    if args.json:
        _emit(args, result)
    else:
        print(f"snapshot {result.snapshot_id}   devices {result.devices}   values {len(result.values)}   "
              f"collection problems {len(result.collection_problems)}")
        _section("Collection problems", [f"{h}: {v}" for h, v in result.collection_problems])
        if not args.apply:
            print("\ndry run; pass --apply to push these to Zabbix")
        else:
            _section("Skipped: no such item in Zabbix (host not synced, or Forward Device template not linked)",
                     result.skipped_hosts)
            print(f"\npushed: processed {result.processed}, rejected by the server {result.rejected}")
            if result.rejected:
                print("rejected values are usually a host the server has not loaded yet (config cache, up to ~60s): run again shortly")
    return 1 if result.rejected else 0


def cmd_enrich(args) -> int:
    notes = api.enrich(forward_network_id(args.network_id), apply=args.apply)
    if args.json:
        _emit(args, notes)
        return 0
    print(f"open problems on Forward-managed hosts needing a note: {len(notes)}")
    for n in notes:
        print(f"\n--- event {n.eventid}: {n.problem}\n{n.text}")
    if notes:
        print("\nadded notes" if args.apply else "\ndry run; pass --apply to add these notes to the problems")
    return 0


def cmd_export_status(args) -> int:
    import csv
    rows = api.export_status(forward_network_id(args.network_id))
    if args.json:
        _emit(args, rows)
        return 0
    out = open(args.output, "w", newline="") if args.output else sys.stdout
    try:
        w = csv.DictWriter(out, fieldnames=list(P.STATUS_COLUMNS), lineterminator="\n")
        w.writeheader()
        w.writerows(rows)
    finally:
        if args.output:
            out.close()
    if args.output:
        print(f"wrote {len(rows)} rows to {args.output}", file=sys.stderr)
    return 0


def cmd_serve(args) -> int:
    from .receiver import Receiver, serve
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(message)s")
    log = logging.getLogger("fwdzbx.receiver")
    raw = args.network_ids or [os.environ.get("FWDZBX_NETWORK_IDS", "")]
    networks = {x.strip() for item in raw for x in item.split(",") if x.strip()}
    settings = ZabbixSettings.from_env()

    def on_snapshot(network_id: str) -> None:
        r = service.push_status(settings, network_id)
        log.info("pushed status for snapshot %s: processed %d rejected %d skipped %d",
                 r.snapshot_id, r.processed, r.rejected, len(r.skipped_hosts))

    def on_values(network_id: str, values: list) -> None:
        processed, rejected, _ = service.send_values(settings, network_id, values)
        log.info("pushed %d event value(s): processed %d rejected %d", len(values), processed, rejected)

    try:
        rx = Receiver(os.environ.get("FWDZBX_WEBHOOK_USER", ""), os.environ.get("FWDZBX_WEBHOOK_PASSWORD", ""),
                      networks, args.path, on_snapshot, on_values)
    except ValueError as e:
        print(f"config error: {e} (set FWDZBX_WEBHOOK_USER, FWDZBX_WEBHOOK_PASSWORD and --network-ids or FWDZBX_NETWORK_IDS)",
              file=sys.stderr)
        return 64
    host, _, port = args.listen.rpartition(":")
    if not port.isdigit():
        print(f"config error: --listen must be HOST:PORT, got {args.listen!r}", file=sys.stderr)
        return 64
    server = serve(rx, host.strip("[]") or "127.0.0.1", int(port))
    print(f"listening on {args.listen}{args.path} for networks {sorted(networks)}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        server.shutdown()
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="fwdzbx", description="Forward <-> Zabbix integration (PoC)")
    ap.add_argument("--network-id", help="Forward network id (or FORWARD_NETWORK_ID)")
    ap.add_argument("--json", action="store_true", help="print machine-readable JSON on stdout and nothing else")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("devices", help="list Forward devices (read-only)").set_defaults(fn=cmd_devices)
    g = sub.add_parser("coverage-gap", help="diff Forward inventory against Zabbix hosts (read-only)")
    g.add_argument("--fail-on-gap", action="store_true", help="exit 1 if any gap is found")
    g.set_defaults(fn=cmd_coverage_gap)
    for name, fn, text in (("sync-inventory", cmd_sync, "create/update Zabbix hosts from Forward"),
                           ("push-status", cmd_push_status, "push Forward collection health to Zabbix trapper items"),
                           ("enrich", cmd_enrich, "add Forward context to open Zabbix problems on managed hosts")):
        p = sub.add_parser(name, help=f"{text} (dry run unless --apply)")
        p.add_argument("--apply", action="store_true")
        p.set_defaults(fn=fn)
    ex = sub.add_parser("export-status", help="CSV of what Zabbix says about each Forward device (read-only)")
    ex.add_argument("--output", "-o", help="write the CSV here instead of stdout")
    ex.set_defaults(fn=cmd_export_status)
    sv = sub.add_parser("serve", help="receive Forward webhooks and push the result to Zabbix")
    sv.add_argument("--listen", default="127.0.0.1:8020")
    sv.add_argument("--path", default="/forward-webhook")
    sv.add_argument("--network-ids", nargs="+", help="only act on these Forward networks (or FWDZBX_NETWORK_IDS)")
    sv.set_defaults(fn=cmd_serve)
    args = ap.parse_args(argv)
    try:
        return args.fn(args)
    except ConfigError as e:
        print(f"config error: {e}", file=sys.stderr)
        return 64
