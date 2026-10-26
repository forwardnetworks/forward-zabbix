# Forward setup

Everything here is optional except the API token: the commands in [Usage](usage.md) work with just a token.
The webhook adds a push so Zabbix learns about a new snapshot within seconds instead of at the next timer run.

## Find the network id

```bash
echo '{}' | fwdctl run inspect-networks
```

## Webhook (snapshot ready)

Forward calls an HTTPS URL when a snapshot is created. Point it at `fwdzbx serve`.

1. Run the receiver ([Run as a service](service.md)) and put TLS in front of it. Forward sends HTTP basic auth,
   so the URL must be HTTPS. For a trial, a Cloudflare quick tunnel works:
   `cloudflared tunnel --url http://127.0.0.1:8020` prints a `https://....trycloudflare.com` URL (it changes on
   every restart; do not use it for anything lasting).
2. In Forward: the organization's webhook settings. URL `https://<host>/forward-webhook`. Authentication:
   basic, with the user and password you gave the receiver. Event type `SNAPSHOT_READY`.
3. **Scope the webhook to your network.** An empty network list means every network in the organization.
4. Use the webhook's test button. A test event for another network is answered `200` and ignored, which
   proves the URL and credentials work.

The same with `fwdctl`. Each call is a dry run until the
input has `"apply": true`; read the plan first.

```bash
echo '{"area":"webhooks","action":"create","name":"forward-zabbix","apply":true,
       "definition":{"url":"https://HOST/forward-webhook","enabled":true,
                     "event_params":{"type":"SNAPSHOT_READY","networkIds":["NETWORK_ID"]}}}' \
  | fwdctl run edit-platform

# then the basic-auth credential, a separate update; the password comes from a mode-600 file
echo '{"area":"webhooks","action":"update","name":"forward-zabbix","apply":true,
       "secret_file":"/path/to/password-file",
       "definition":{"credential_type":"BASIC_AUTH","credential_username":"forward"}}' \
  | fwdctl run edit-platform
```

`fwdctl run edit-platform --help` lists the current fields. Undo is `action: delete` with
`confirm` set to the webhook name.

Behaviors that matter (from Forward's source and from testing):

- Fires only when a snapshot is **created**. No retry on failure, and a 10 second timeout. The receiver
  answers within milliseconds and does its work in a queue.
- A deployment property `OUTBOUND_CONNECTIONS` must allow outbound calls; org admins cannot change it, and
  when it is off Forward silently sends nothing. If no event ever arrives, ask whoever runs your Forward.
- Because delivery is not guaranteed, keep the `push-status` timer as a fallback.

## Zabbix data in NQE

`fwdzbx export-status -o zabbix-status.csv` produces a CSV Forward can hold as a **data file**. Uploading it
changes Forward and is optional. A data file is organization-wide and read in a query as
`network.extensions.<nqe_name>`:

```bash
fwdctl run edit-data-file < upload.json     # dry run; {"action":"upload","name":"zabbix-status.csv",
                                            #  "file_type":"CSV","nqe_name":"zabbix_status","file_path":"..."}
```

then attach it to a network with `action: attach`. Re-run `export-status` and `action: replace` to refresh it;
nothing refreshes it automatically. Undo is `action: delete` with `confirm` set to the name (clean only while
no network has it attached).

## Collector

A snapshot needs a collector that can reach your devices. This is ordinary Forward setup and not part of this
project; see Forward's documentation.

## Screenshots

This repository ships no screenshots of the Forward UI. Add your own under `docs/images/`.
