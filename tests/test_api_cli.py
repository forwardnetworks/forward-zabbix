"""The JSON contract pipelines depend on: stdout is valid JSON only, and exit codes mean what the docs say."""
import json

import fwdzbx
from fwdzbx import api, cli
from fwdzbx.model import FwdDevice
from fwdzbx.plan import Gap, SyncPlan, desired_host


def dev(name="a", ip="10.0.0.1"):
    return FwdDevice(name=name, site="s", vendor="V", model="m", os="o", os_version="1", device_type="SWITCH", ips=(ip,))


def test_results_serialise_to_plain_json_including_sets_and_nested_dataclasses():
    plan = SyncPlan(create=[desired_host(dev(), "1")])
    out = json.loads(json.dumps(api.to_jsonable(api.InventorySync(plan))))
    created, = out["plan"]["create"]
    assert created["host"] == "a" and created["groups"] == sorted(created["groups"]) and out["applied"] is None


def test_json_flag_prints_only_json_on_stdout(monkeypatch, capsys):
    monkeypatch.setattr(api, "coverage_gap", lambda network_id: Gap(unmonitored=["x"], matched=3))
    assert cli.main(["--network-id", "1", "--json", "coverage-gap"]) == 0
    out = capsys.readouterr().out
    assert json.loads(out)["unmonitored"] == ["x"]


def test_fail_on_gap_exits_1_only_when_there_is_a_gap(monkeypatch, capsys):
    monkeypatch.setattr(api, "coverage_gap", lambda network_id: Gap(unmonitored=["x"]))
    assert cli.main(["--network-id", "1", "coverage-gap", "--fail-on-gap"]) == 1
    monkeypatch.setattr(api, "coverage_gap", lambda network_id: Gap(matched=2))
    assert cli.main(["--network-id", "1", "coverage-gap", "--fail-on-gap"]) == 0


def test_push_status_exit_code_is_1_when_zabbix_rejects_values(monkeypatch, capsys):
    result = api.StatusPush("9", 2, [], [], pushed=True, processed=1, rejected=1)
    monkeypatch.setattr(api, "push_status", lambda network_id, apply=False: result)
    assert cli.main(["--network-id", "1", "push-status", "--apply"]) == 1


def test_missing_network_id_is_a_usage_error_not_a_traceback(monkeypatch, capsys):
    monkeypatch.delenv("FORWARD_NETWORK_ID", raising=False)
    assert cli.main(["coverage-gap"]) == 64
    assert "network-id" in capsys.readouterr().err


def test_package_exports_the_api():
    assert {"coverage_gap", "sync_inventory", "push_status", "enrich", "devices"} <= set(fwdzbx.__all__)
