"""Receiver behaviour from the requirements: authenticate, validate, scope to configured networks, ack fast."""
import base64
import http.client
import json
import threading

import pytest

from fwdzbx.receiver import Receiver, failure_values, parse_event, serve

AUTH = "Basic " + base64.b64encode(b"hook:s3cret").decode()


@pytest.fixture
def rx():
    calls = {"snapshot": [], "values": []}
    done = threading.Event()

    def on_snapshot(n):
        calls["snapshot"].append(n)
        done.set()

    def on_values(n, v):
        calls["values"].append((n, v))
        done.set()

    receiver = Receiver("hook", "s3cret", {"7"}, "/hook", on_snapshot, on_values)
    server = serve(receiver, "127.0.0.1", 0)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield receiver, server.server_address[1], calls, done
    server.shutdown()


def post(port, body, auth=AUTH, path="/hook"):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    headers = {"Content-Type": "application/json"}
    if auth:
        headers["Authorization"] = auth
    conn.request("POST", path, body if isinstance(body, bytes) else json.dumps(body), headers)
    r = conn.getresponse()
    return r.status, r.read().decode()


def event(**kw):
    return {"type": "SNAPSHOT_READY", "networkId": "7", "snapshotId": "100", **kw}


def test_rejects_missing_and_wrong_credentials(rx):
    _, port, calls, _ = rx
    assert post(port, event(), auth=None)[0] == 401
    assert post(port, event(), auth="Basic " + base64.b64encode(b"hook:wrong").decode())[0] == 401
    assert post(port, event(), auth="Basic !!!not-base64")[0] == 401
    assert not calls["snapshot"]


def test_rejects_bad_input_without_queuing_work(rx):
    receiver, port, _, _ = rx
    assert post(port, b"not json")[0] == 400
    assert post(port, {"type": "SNAPSHOT_READY"})[0] == 400          # missing networkId/snapshotId
    assert post(port, event(), path="/elsewhere")[0] == 404
    assert post(port, b"x" * (64 * 1024 + 1))[0] == 413
    assert receiver.jobs.empty()


def test_snapshot_ready_for_a_configured_network_is_acknowledged_and_handled(rx):
    _, port, calls, done = rx
    status, text = post(port, event())
    assert (status, text) == (202, "queued")
    assert done.wait(5) and calls["snapshot"] == ["7"]


def test_events_for_other_networks_are_ignored(rx):
    receiver, port, calls, _ = rx
    status, text = post(port, event(networkId="999"))
    assert status == 200 and "not configured" in text and receiver.jobs.empty()


def test_the_same_snapshot_event_is_handled_once(rx):
    receiver, port, _, done = rx
    assert post(port, event())[1] == "queued"
    assert done.wait(5)
    assert "duplicate" in post(port, event())[1]


def test_check_failure_becomes_a_value_on_the_network_host(rx):
    _, port, calls, done = rx
    payload = {"type": "INTENT_VERIFICATION_FAILURE", "networkId": "7", "snapshotId": "100",
               "checkName": "no-telnet", "checkPath": "/security", "failureType": "VIOLATED"}
    assert post(port, payload)[0] == 202 and done.wait(5)
    (network, values), = calls["values"]
    assert network == "7" and values == [("forward-network-7", "fwd.check.failure", "no-telnet: VIOLATED [snapshot 100]")]


def test_unknown_event_types_are_acknowledged_but_not_acted_on(rx):
    receiver, port, _, _ = rx
    status, text = post(port, event(type="SOMETHING_NEW"))
    assert status == 200 and "event type" in text and receiver.jobs.empty()


def test_will_not_start_without_credentials_or_networks():
    with pytest.raises(ValueError):
        Receiver("", "x", {"1"}, "/h", lambda n: None, lambda n, v: None)
    with pytest.raises(ValueError):
        Receiver("u", "", {"1"}, "/h", lambda n: None, lambda n, v: None)
    with pytest.raises(ValueError):
        Receiver("u", "p", set(), "/h", lambda n: None, lambda n, v: None)


def test_failure_value_includes_violation_count_when_present():
    ev = parse_event(json.dumps({"type": "NQE_VERIFICATION_FAILURE", "networkId": "7", "snapshotId": "5",
                                 "checkName": "ntp", "numViolations": 3}).encode())
    assert failure_values(ev)[0][2] == "ntp: failing (3 violations) [snapshot 5]"


def test_other_methods_get_404_not_501(rx):
    _, port, _, _ = rx
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    conn.request("GET", "/")
    assert conn.getresponse().status == 404
