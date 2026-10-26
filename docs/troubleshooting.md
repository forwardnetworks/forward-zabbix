# Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `fwdzbx` exits 64 with a missing-variable message | A required [environment variable](configuration.md) is not set. Nothing else was attempted |
| Zabbix: `Unable to determine current Zabbix database version` | The frontend is up but the database schema is still loading (fresh container). Wait and retry |
| `push-status` reports hosts "skipped" | They do not exist in Zabbix yet. Run `sync-inventory --apply` first |
| Values rejected right after creating hosts | The Zabbix server loads new hosts into its configuration cache about once a minute. Retry |
| Sync lists **collisions** | A device's name or IP already belongs to a host this tool did not create. It is left alone. Rename or tag that host, or remove it yourself, then sync again |
| Sync lists **stale** hosts | A managed host's device left Forward. Never deleted automatically; delete it in Zabbix if it is really gone |
| Webhook returns `401` | The basic-auth user or password in Forward differs from `FWDZBX_WEBHOOK_*` |
| Webhook returns `404` | Wrong path (default `/forward-webhook`), or a method other than POST |
| Forward's test works but no real event ever arrives | Forward delivers only on snapshot creation, only for networks in the webhook's scope, and only if the deployment allows outbound calls (`OUTBOUND_CONNECTIONS`, not changeable by org admins). The receiver log shows each event it gets |
| Event arrives, nothing changes in Zabbix | `FWDZBX_NETWORK_IDS` does not include that network, so it is ignored; the log says so |
| Trapper pushes go nowhere | `ZABBIX_SERVER` / `ZABBIX_TRAPPER_PORT` point at the frontend, not the server's trapper port |
| A "check failing" problem stays open | Expected until `{$FWD.CHECK_FAILURE.WINDOW}` passes with no new failure event |
| A "collection failed" problem on a device | Forward could not log in or reach it. Open the device's problem for the reason and Forward's collection log |

For Forward-side diagnosis (collector down, failed collections), use
`fwdctl`: `fwdctl run inspect-collection` and
`fwdctl run investigate-collection-failure`.
