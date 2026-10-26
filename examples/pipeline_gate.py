"""Use fwdzbx inside a Python pipeline: fail the run if Forward and Zabbix disagree.

    export FORWARD_URL=... FORWARD_USERNAME=... FORWARD_PASSWORD=...
    export ZABBIX_URL=... ZABBIX_TOKEN=...
    python examples/pipeline_gate.py <forward-network-id>
"""
import sys

import fwdzbx


def main(network_id: str) -> int:
    gap = fwdzbx.coverage_gap(network_id)  # read-only
    print(f"matched={gap.matched} unmonitored={len(gap.unmonitored)} "
          f"unmodeled={len(gap.unmodeled)} drift={len(gap.drift)}")
    if gap.unmonitored:
        print("not in Zabbix:", ", ".join(gap.unmonitored))
        return 1
    plan = fwdzbx.sync_inventory(network_id).plan  # dry run: nothing is changed
    print(f"a sync would create {len(plan.create)} and update {len(plan.update)} hosts")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
