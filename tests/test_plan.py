"""Derived from the requirements in DESIGN.md, using synthetic devices only."""
from fwdzbx.model import FwdDevice, Link
from fwdzbx.plan import DEVICE_TEMPLATE, MANAGED_BY, ZbxHost, coverage_gap, desired_host, enrichment_note, plan_sync, status_values


def dev(name, ip, site="dc1", vendor="ARISTA"):
    return FwdDevice(name=name, site=site, vendor=vendor, model="m", os="eos", os_version="1",
                     device_type="SWITCH", ips=(ip,) if ip else ())


def host(hid, name, ip, groups=(), tags=(), templates=()):
    return ZbxHost(hostid=hid, host=name, name=name, ips=(ip,), groups=frozenset(groups),
                   tags=tuple(tags), interfaceid=f"i{hid}", templates=frozenset(templates))


MANAGED = (("managed-by", MANAGED_BY), ("fwd-network", "1"))


def test_gap_finds_unmonitored_unmodeled_and_drift():
    devices = [dev("a", "10.0.0.1"), dev("b", "10.0.0.2"), dev("c", "10.0.0.3"), dev("d", "10.0.0.4")]
    hosts = [host("1", "a", "10.0.0.1"),            # clean
             host("2", "B", "10.9.9.9"),            # same name (case-insensitive), wrong IP
             host("3", "c-renamed", "10.0.0.3"),    # same IP, different name
             host("4", "extra", "10.0.0.50"),       # not in Forward
             host("5", "Zabbix server", "127.0.0.1", groups={"Zabbix servers"})]  # ignored
    gap = coverage_gap(devices, hosts)
    assert gap.unmonitored == ["d"]
    assert gap.unmodeled == ["extra"]
    assert gap.matched == 3
    assert {n for n, _ in gap.drift} == {"b", "c"}


def test_device_without_ip_is_not_synced():
    plan = plan_sync([dev("noip", None)], [], "1")
    assert plan.skipped_no_ip == ["noip"] and not plan.create


def test_sync_never_touches_unmanaged_hosts():
    plan = plan_sync([dev("a", "10.0.0.1"), dev("b", "10.0.0.2")],
                     [host("1", "a", "10.0.0.1"),                 # unmanaged, same name
                      host("2", "other", "10.0.0.2")], "1")       # unmanaged, owns b's IP
    assert not plan.create and not plan.update
    assert {n for n, _ in plan.collisions} == {"a", "b"}


def test_sync_idempotent_after_create():
    d = dev("a", "10.0.0.1")
    want = desired_host(d, "1")
    existing = host("1", "a", "10.0.0.1", groups=want.groups, tags=want.tags, templates=want.templates)
    plan = plan_sync([d], [existing], "1")
    assert plan.unchanged == ["a"] and not plan.create and not plan.update


def test_sync_updates_changed_ip_and_reports_stale_without_deleting():
    d = dev("a", "10.0.0.9")
    want = desired_host(d, "1")
    plan = plan_sync([d], [host("1", "a", "10.0.0.1", groups=want.groups, tags=want.tags, templates=want.templates),
                           host("2", "gone", "10.0.0.7", tags=MANAGED)], "1")
    assert len(plan.update) == 1 and "ip" in plan.update[0][2][0]
    assert plan.stale == ["gone"]


def test_host_name_sanitised_but_visible_name_kept():
    want = desired_host(dev("core/01 (lab)", "10.0.0.1"), "1")
    assert want.host == "core_01 _lab_" and want.name == "core/01 (lab)"


def test_managed_host_without_the_device_template_gets_it_added():
    d = dev("a", "10.0.0.1")
    want = desired_host(d, "1")
    plan = plan_sync([d], [host("1", "a", "10.0.0.1", groups=want.groups, tags=want.tags)], "1")
    assert len(plan.update) == 1 and plan.update[0][2] == ["templates"]


def test_foreign_templates_on_a_managed_host_are_not_a_change():
    d = dev("a", "10.0.0.1")
    want = desired_host(d, "1")
    existing = host("1", "a", "10.0.0.1", groups=want.groups, tags=want.tags,
                    templates={DEVICE_TEMPLATE, "Some Other Template"})
    plan = plan_sync([d], [existing], "1")
    assert plan.unchanged == ["a"] and not plan.update


def test_status_values_report_failures_and_skip_devices_with_no_result():
    ok = dev("ok", "10.0.0.1")
    bad = FwdDevice(**{**ok.__dict__, "name": "bad", "collected": "collectionFailed:AUTHENTICATION_FAILED"})
    ok = FwdDevice(**{**ok.__dict__, "collected": "completed"})
    unknown = dev("unknown", "10.0.0.3")  # no collection result at all
    rows = status_values([ok, bad, unknown], "7", "99", 1700000000)
    assert ("forward-network-7", "fwd.devices.total", "3") in rows
    assert ("forward-network-7", "fwd.devices.collection_failed", "1") in rows
    assert ("forward-network-7", "fwd.snapshot.id", "99") in rows
    assert ("bad", "fwd.collection.result", "collectionFailed:AUTHENTICATION_FAILED") in rows
    assert not any(h == "unknown" for h, _, _ in rows)


def test_note_names_neighbors_once_with_their_ports_and_starts_with_the_marker():
    d = FwdDevice(**{**dev("core1", "10.0.0.1").__dict__, "collected": "completed"})
    note = enrichment_note(d, [Link("Eth1", "rtr-b", "Eth9"), Link("Eth2", "rtr-b", "Eth10"), Link("Eth3", "rtr-a", "Eth1")], "42")
    assert note.startswith("[Forward]") and "snapshot 42" in note
    assert "2 device(s) over 3 link(s)" in note
    assert note.count("rtr-b:") == 1 and "Eth1<->Eth9, Eth2<->Eth10" in note
    assert note.index("rtr-a:") < note.index("rtr-b:")


def test_note_says_so_when_forward_could_not_collect_the_device():
    d = FwdDevice(**{**dev("fw1", "10.0.0.2").__dict__, "collected": "collectionFailed:AUTHENTICATION_FAILED"})
    note = enrichment_note(d, [], "42")
    assert "could not collect" in note and "AUTHENTICATION_FAILED" in note and "no links" in note


def test_note_fits_the_zabbix_message_limit_for_a_device_with_many_neighbors():
    links = [Link(f"Eth{i}", f"peer-number-{i:04d}", "Eth1") for i in range(400)]
    assert len(enrichment_note(dev("hub", "10.0.0.3"), links, "1")) <= 2048


def test_the_network_carrier_host_is_not_stale_and_not_unmodeled():
    carrier = host("9", "forward-network-1", "127.0.0.1", tags=MANAGED)
    assert plan_sync([], [carrier], "1").stale == []
    assert coverage_gap([], [carrier]).unmodeled == []


def test_a_user_host_that_merely_looks_like_the_carrier_is_still_reported():
    lookalike = host("9", "forward-network-1", "10.9.9.9")  # not managed by this tool
    assert coverage_gap([], [lookalike]).unmodeled == ["forward-network-1"]


def test_status_rows_match_by_name_then_ip_and_count_problems():
    from fwdzbx.plan import ZbxHost, status_rows
    devs = [FwdDevice("core-1", "dc1", "ARISTA", "m", "EOS", "1", "SWITCH", ("10.0.0.1",), (), "completed"),
            FwdDevice("edge-1", "dc1", "ARISTA", "m", "EOS", "1", "SWITCH", ("10.0.0.2",), (), "completed"),
            FwdDevice("lonely", "dc1", "ARISTA", "m", "EOS", "1", "SWITCH", ("10.0.0.9",), (), "completed")]
    hosts = [ZbxHost("1", "core-1", "core-1", ("10.0.0.1",), frozenset(), ()),
             ZbxHost("2", "old-name", "old-name", ("10.0.0.2",), frozenset(), ())]
    rows = status_rows(devs, hosts, {"core-1": (2, 4), "old-name": (1, 2)})
    assert rows == [
        {"device": "core-1", "monitored": "yes", "zabbix_host": "core-1", "open_problems": "2", "worst_severity": "high"},
        {"device": "edge-1", "monitored": "yes", "zabbix_host": "old-name", "open_problems": "1", "worst_severity": "warning"},
        {"device": "lonely", "monitored": "no", "zabbix_host": "", "open_problems": "0", "worst_severity": ""},
    ]
