#!/usr/bin/env python3
"""Compare direct-local and strict-SSH Valkey Store Transition matrix runs (issue #101).

Usage: valkey-transition-parity.py DEST SOURCE_VERSION DIRECT_ROOT SSH_ROOT MODE...
Accounting must be identical per mode. The only field not compared byte-for-byte is the replication
offset at the fence, which advances with wall-clock PINGs; offset equality and link state are compared.
"""
import json
import sys
from pathlib import Path

dest, version, droot, sroot = Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]), Path(sys.argv[4])
load = lambda root, mode: json.loads((root / mode / "support-observation.json").read_text())


def normalized(accounting):
    accounting = dict(accounting)
    fence = accounting.pop("replicationAtFence")
    accounting["replicationAtFence"] = {
        "offsetsEqual": fence["activeOffset"] == fence["candidateOffset"], "linkUp": fence["linkUp"],
    }
    return accounting


rows = []
for mode in sys.argv[5:]:
    d, r = load(droot, mode), load(sroot, mode)
    checks = {
        "transportsDiffer": d["transport"] == "direct-local" and r["transport"] == "ssh",
        "bothPassed": d["result"] == r["result"] == "passed",
        "hostPlatformMatches": all(d[k] == r[k] for k in ("kernel", "os", "osVersion", "architecture", "systemdVersion")),
        "executorDigestMatches": d["executorDigest"] == r["executorDigest"],
        "accountingMatches": normalized(d["accounting"]) == normalized(r["accounting"]),
    }
    if not all(checks.values()):
        raise SystemExit(f"{mode} parity check failed: {checks!r}")
    rows.append({"faultMode": mode, "checks": checks, "accounting": r["accounting"], "executorDigest": r["executorDigest"], "kernel": r["kernel"]})
dest.write_text(json.dumps({
    "schemaVersion": "provision.dev/valkey-transition-ssh-parity/v1alpha1", "sourceVersion": version, "comparisons": rows,
    "supportBoundaries": [
        "single Host Target", "forward-only rollback classification",
        "authoritative Store Data Role only", "same-major transition only",
        "no crash-durability, host-loss, backup or restore claim",
    ],
}, indent=2, sort_keys=True) + "\n")
