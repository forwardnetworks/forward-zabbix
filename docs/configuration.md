# Configuration

Everything comes from environment variables or command-line flags. Nothing is read from or written to disk
by the tool itself, so secrets stay wherever you keep them (a systemd `EnvironmentFile`, a container
`--env-file`, a secret store).

## Forward

| Variable | Required | Meaning |
|---|---|---|
| `FORWARD_URL` | yes | Instance URL, for example `https://fwd.app` |
| `FORWARD_USERNAME` | yes | API token access key (or a login name) |
| `FORWARD_PASSWORD` | yes | API token secret (or a password) |
| `FORWARD_NETWORK_ID` | one of this or `--network-id` | The Forward network to act on |

The Forward SDK also honours `FORWARD_VERIFY_TLS` (`false`, or a path to a CA bundle for a private CA),
`FORWARD_TIMEOUT`, `FORWARD_RETRIES` and `FORWARD_RATE_LIMIT_RPM`. See the
[Forward Python SDK configuration](https://github.com/forwardnetworks/forward-python-sdk).

## Zabbix

| Variable | Required | Meaning |
|---|---|---|
| `ZABBIX_URL` | yes | Frontend URL, for example `https://zabbix.example.com` (the API path is added) |
| `ZABBIX_TOKEN` | one of token or user+password | Zabbix API token (preferred) |
| `ZABBIX_USER`, `ZABBIX_PASSWORD` | | Login instead of a token |
| `ZABBIX_SERVER` | no | Trapper host. Default: the host of `ZABBIX_URL` |
| `ZABBIX_TRAPPER_PORT` | no | Trapper port. Default `10051` |

If your frontend and server run on different hosts or in containers, set `ZABBIX_SERVER` explicitly: values
are pushed to the **server's trapper port**, not the web frontend.

## Webhook receiver (`fwdzbx serve`)

| Variable / flag | Required | Meaning |
|---|---|---|
| `FWDZBX_WEBHOOK_USER` | yes | Basic-auth user Forward must send |
| `FWDZBX_WEBHOOK_PASSWORD` | yes | Basic-auth password. The receiver refuses to start without both |
| `FWDZBX_NETWORK_IDS` / `--network-ids` | yes | Forward network ids to act on, comma-separated in the variable; the flag takes space- or comma-separated ids. Events for any other network are acknowledged and ignored |
| `--listen HOST:PORT` | no | Default `127.0.0.1:8020` |
| `--path PATH` | no | Default `/forward-webhook`. Any other path returns 404 |

## Global flags

| Flag | Meaning |
|---|---|
| `--network-id ID` | Overrides `FORWARD_NETWORK_ID` |
| `--json` | Print machine-readable JSON on stdout and nothing else. See [Python API](python-api.md) |
| `--apply` | On `sync-inventory`, `push-status`, `enrich`: make the change. Without it they only report |

## Zabbix template macros

| Macro | Default | Meaning |
|---|---|---|
| `{$FWD.SNAPSHOT.MAX_AGE}` | `36h` | Raise "no new snapshot" when no push reports a newer one for this long |
| `{$FWD.CHECK_FAILURE.WINDOW}` | `6h` | A check-failure problem clears after this long with no failure event. Set it a little over your collection interval; Forward reports failures, never recoveries |
| `{$SNMP_COMMUNITY}` | not set | Referenced by the SNMP interface on created hosts; set it only if you attach SNMP polling |
