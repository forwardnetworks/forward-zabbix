"""Receives Forward webhooks. Stdlib only. Acknowledges fast and works off a queue, because Forward does not retry."""
from __future__ import annotations

import base64
import hmac
import json
import logging
import queue
import threading
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Callable

from .plan import network_host_name

log = logging.getLogger("fwdzbx.receiver")
MAX_BODY = 64 * 1024
SNAPSHOT_READY = "SNAPSHOT_READY"
CHECK_FAILURES = ("INTENT_VERIFICATION_FAILURE", "NQE_VERIFICATION_FAILURE")


@dataclass(frozen=True)
class Event:
    type: str
    network_id: str
    snapshot_id: str
    raw: dict


def parse_event(body: bytes) -> Event:
    """Raises ValueError on anything that is not a Forward event we understand."""
    try:
        data = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError) as e:
        raise ValueError(f"not JSON: {e}") from None
    if not isinstance(data, dict):
        raise ValueError("payload is not a JSON object")
    missing = [k for k in ("type", "networkId", "snapshotId") if not data.get(k)]
    if missing:
        raise ValueError(f"missing field(s): {', '.join(missing)}")
    return Event(str(data["type"]), str(data["networkId"]), str(data["snapshotId"]), data)


def authorized(header: str | None, user: str, password: str) -> bool:
    if not header or not header.startswith("Basic "):
        return False
    try:
        given = base64.b64decode(header[6:], validate=True).decode()
    except (ValueError, UnicodeDecodeError):
        return False
    return hmac.compare_digest(given.encode(), f"{user}:{password}".encode())


def failure_values(event: Event) -> list[tuple[str, str, str]]:
    """A check-failure event as one trapper value on the network host."""
    r = event.raw
    detail = f"{r.get('checkName') or r.get('checkPath') or 'check'}: {r.get('failureType') or r.get('checkStatus') or 'failing'}"
    if r.get("numViolations") is not None:
        detail += f" ({r['numViolations']} violations)"
    return [(network_host_name(event.network_id), "fwd.check.failure", f"{detail} [snapshot {event.snapshot_id}]")]


class Receiver:
    def __init__(self, user: str, password: str, networks: set[str], path: str,
                 on_snapshot: Callable[[str], None], on_values: Callable[[str, list], None]):
        if not user or not password:
            raise ValueError("a webhook user and password are required")
        if not networks:
            raise ValueError("at least one network id is required")
        self.user, self.password, self.networks, self.path = user, password, networks, path
        self.on_snapshot, self.on_values = on_snapshot, on_values
        self.jobs: queue.Queue = queue.Queue()
        self._seen: set[tuple[str, str]] = set()

    def accept(self, event: Event) -> str:
        """Decide what to do with a verified event; returns a short outcome for the log and the response."""
        if event.network_id not in self.networks:
            return "ignored: network not configured"
        key = (event.type, event.snapshot_id + ":" + str(event.raw.get("checkPath", "")))
        if key in self._seen:
            return "ignored: duplicate"
        self._seen.add(key)
        if event.type == SNAPSHOT_READY:
            self.jobs.put(lambda: self.on_snapshot(event.network_id))
        elif event.type in CHECK_FAILURES:
            self.jobs.put(lambda: self.on_values(event.network_id, failure_values(event)))
        else:
            return f"ignored: event type {event.type}"
        return "queued"

    def worker(self) -> None:
        while True:
            job = self.jobs.get()
            if job is None:
                return
            try:
                job()
            except Exception:  # one bad event must not stop the worker
                log.exception("event handling failed")

    def handler(self):
        outer = self

        class H(BaseHTTPRequestHandler):
            timeout = 10  # a stalled client must not hold a thread forever

            def log_message(self, fmt, *args):  # never log headers or bodies
                log.info("%s %s", self.command, self.path)

            def _reply(self, code: int, text: str) -> None:
                data = text.encode()
                self.send_response(code)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self):
                if self.path != outer.path:
                    return self._reply(404, "not found")
                if not authorized(self.headers.get("Authorization"), outer.user, outer.password):
                    return self._reply(401, "unauthorized")
                try:
                    length = int(self.headers.get("Content-Length", "0"))
                except ValueError:
                    return self._reply(400, "bad Content-Length")
                if length <= 0 or length > MAX_BODY:
                    return self._reply(413 if length > MAX_BODY else 400, "bad body size")
                try:
                    event = parse_event(self.rfile.read(length))
                except ValueError as e:
                    return self._reply(400, str(e))
                outcome = outer.accept(event)
                log.info("%s network=%s snapshot=%s -> %s", event.type, event.network_id, event.snapshot_id, outcome)
                self._reply(202 if outcome == "queued" else 200, outcome)

            def _not_found(self):  # only POST to the webhook path does anything
                self._reply(404, "not found")

            do_GET = do_HEAD = do_PUT = do_DELETE = do_PATCH = _not_found

        return H


def serve(receiver: Receiver, host: str, port: int) -> ThreadingHTTPServer:
    server = ThreadingHTTPServer((host, port), receiver.handler())
    threading.Thread(target=receiver.worker, daemon=True).start()
    return server
