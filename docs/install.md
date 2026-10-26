# Install

## Requirements

- Python 3.12 or newer, or Docker.
- A Forward instance and an API token (below).
- Zabbix 7.0 (tested on 7.0.32). Older versions are untested; the template export format is `7.0`.

## Get the tool

From a clone of this repository:

```bash
uv sync                    # development install into .venv
uv run fwdzbx --help
```

As an installed command (for a server):

```bash
python3 -m venv /opt/fwdzbx
/opt/fwdzbx/bin/pip install /path/to/forward-zabbix     # or: pip install git+https://github.com/forwardnetworks/forward-zabbix
/opt/fwdzbx/bin/fwdzbx --help
```

As a container: `docker build -t fwdzbx .` (see [Run as a service](service.md#container)).

## Forward credentials

Create an API token in Forward and use its **access key as the username and its secret as the password**
(`FORWARD_USERNAME` / `FORWARD_PASSWORD`). `fwdzbx` only reads Forward: it runs NQE queries and lists
snapshots. Give the token read access to the network you will sync; it needs no write role.

You also need the **network id** (`FORWARD_NETWORK_ID` or `--network-id`). With
`fwdctl` (Forward's command-line tool) installed:

```bash
echo '{}' | fwdctl run inspect-networks
```

or read it from the URL in the Forward UI.

## Zabbix credentials

Create an **API token** in Zabbix (Users > API tokens) for a dedicated user, and use it as `ZABBIX_TOKEN`.
(A user and password also work: `ZABBIX_USER` / `ZABBIX_PASSWORD`.)

What the user must be able to do, from what the tool calls: read and create hosts, read and create host
groups, read templates and items, link templates to hosts, read problems and events, and add a message to an
event acknowledgement.

**Permissions are only partly tested.** Every test used Zabbix's built-in `Admin` (Super admin) account. A narrower
role with read-write access to the `Forward` host groups should be enough but has not been tried.

## Next

1. [Import the Zabbix templates](zabbix-setup.md#import-the-templates).
2. Run `fwdzbx --network-id <id> coverage-gap` to confirm both credentials work. It changes nothing.
3. Run `fwdzbx --network-id <id> sync-inventory` (a dry run), then with `--apply`.
