# Change evidence

After a change window, `fwdzbx change-evidence` answers two questions from both tools at once: **did the network change
the way it was meant to**, and **what did monitoring raise while it happened**.

It is **read-only**. It reads Forward and Zabbix and changes neither. It does not start a collection: the "after"
snapshot has to be one Forward has already taken, and Forward collects on its own schedule.

## Use

Name the change in one of three ways:

```bash
# two snapshots you already have
fwdzbx --network-id <id> change-evidence --before 1001 --after 1002 --expect core-1 --expect edge-2

# a Zabbix maintenance: its active period is the window, its hosts are what was meant to change
fwdzbx --network-id <id> change-evidence --maintenance "Core upgrade"

# a window by time
fwdzbx --network-id <id> change-evidence --since 2026-01-31T22:00:00Z --until 2026-01-31T23:30:00Z --expect core-1
```

With a window, "before" is the newest processed snapshot taken at or before its start, and "after" the oldest taken at
or after its end. If Forward has not collected since the window ended, the command says so; run it again once it has.

`--expect DEVICE` (repeatable) names the devices the change was meant to alter. With `--maintenance` and no `--expect`,
the devices behind the maintenance's hosts (named directly or through their host groups) are the expectation. Add
`--fail-on-diff` to exit 1 when the change did not match, which makes it a gate in a pipeline. `--json` prints only JSON.

## What it reports

- **Configuration**: the devices whose collected configuration differs between the two snapshots, set against the
  expectation: *changed but not expected* and *expected but did not change*. The verdict is `as expected`,
  `differs from expectation`, or `no expectation given` (nothing to judge against, so changes are listed, not flagged).
- **Other Forward changes** between the snapshots, as Forward counts them with state-file churn left out, and **check
  results that changed**. Counts marked as at least a figure are lower bounds.
- **Zabbix problems** that opened inside the window on hosts this tool manages, including ones that **recovered** and
  ones held back by a maintenance (Zabbix drops that mark once the maintenance ends, so a run after the window rarely shows it). A problem that opened during a change and cleared is part of what the change did, so
  recovered ones are listed.

## Limits

- Forward compares the two snapshots **as collected**. A change made and reverted between them does not appear.
- A maintenance's active period is used as the window. A recurring maintenance is active for longer than any one
  occurrence, so pass `--since` and `--until` to name the occurrence you mean.
- Problems cover hosts this tool manages (`sync-inventory`), for the network given, including hosts disabled since. A
  problem on a trigger that has since been deleted cannot be tied to a host; the report counts those in a note.
- Snapshots must be processed and not archived; an archived id is reported as not found.
- With `--maintenance`, a maintenance host that matches no Forward device is named in a note and left out of the
  expectation. If none match there is no expectation, the verdict is `no expectation given`, and `--fail-on-diff` does not
  fail, so give `--expect` when gating.
- It reads Forward's file comparison for configuration; it lists which devices changed, not the changed lines. Open the
  device in Forward for the lines.
- Zabbix 7.0 has no event for a maintenance starting or ending, so nothing here runs on its own; run the command after
  the window, by hand or from your change pipeline.
