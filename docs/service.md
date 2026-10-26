# Run as a service

Two long-lived shapes: the **receiver** (`fwdzbx serve`, listens for Forward's webhook) and **timers** that run
the one-shot commands. Use both: the receiver for speed, the timers because webhooks are not retried.

## systemd

Units are in `deploy/systemd/`.

```bash
go build -o fwdzbx ./cmd/fwdzbx && sudo install -m 0755 fwdzbx /usr/local/bin/fwdzbx
sudo install -d -m 0750 /etc/fwdzbx
sudo install -m 0600 deploy/systemd/fwdzbx.env.example /etc/fwdzbx/fwdzbx.env   # then edit it
sudo cp deploy/systemd/fwdzbx-* /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now fwdzbx-receiver.service \
     fwdzbx-sync-inventory.timer fwdzbx-push-status.timer fwdzbx-enrich.timer
```

| Unit | Runs | Schedule |
|---|---|---|
| `fwdzbx-receiver.service` | `fwdzbx serve` on `127.0.0.1:8020` | always, restarts on failure |
| `fwdzbx-sync-inventory.timer` | `sync-inventory --apply` | daily |
| `fwdzbx-push-status.timer` | `push-status --apply` | every 30 minutes |
| `fwdzbx-enrich.timer` | `enrich --apply` | every 5 minutes |
| `fwdzbx-push-perf.timer` | `push-perf --apply` (optional; see [Performance data](performance.md)) | every 5 minutes |

All units run as a transient unprivileged user (`DynamicUser`) with no capabilities, a read-only filesystem (except the `push-perf` state directory)
and only IP sockets. The env file is read by systemd as root, so the service user never needs access to it.
Look at a run with `journalctl -u fwdzbx-receiver`.

The receiver listens on loopback. Put a TLS-terminating reverse proxy (or a tunnel) in front of it and
forward `/forward-webhook` only.

What was verified (on the earlier Python build; the Go units, which run `/usr/local/bin/fwdzbx`, have not been
re-run): the receiver was run as a systemd user service under the same sandbox properties (no new privileges,
read-only system, private tmp and devices, restricted address families), where it rejected unauthenticated
calls and pushed status for a real event. The system-level units, the timers and the `DynamicUser` setting
were not run end to end.

## Container

```bash
docker build -t fwdzbx .
docker run -d --name fwdzbx --restart unless-stopped --env-file /etc/fwdzbx/fwdzbx.env \
  -p 127.0.0.1:8020:8020 --read-only --cap-drop ALL --security-opt no-new-privileges \
  fwdzbx serve --listen 0.0.0.0:8020
```

The image runs as a non-root user. Run the timers' commands the same way, for example with cron:
`docker run --rm --env-file /etc/fwdzbx/fwdzbx.env fwdzbx push-status --apply`.

The Go image builds, starts, and runs as uid 65532 (distroless `nonroot`). Serving a webhook from it has not been
re-run; the earlier Python image, under the flags above, answered `401` without credentials and pushed status for a real event.
In a container, `ZABBIX_URL` is the frontend as the container sees it, and `ZABBIX_SERVER` the server's
name or address for the trapper port.

## Upgrades

Rebuild and re-install the binary, then `systemctl restart fwdzbx-receiver`. Re-import the Zabbix templates when
`zabbix/forward-templates.yaml` changes; importing updates in place.
