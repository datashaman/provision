#!/usr/bin/env python3
"""Normalized logical accounting of one PostgreSQL acceptance run (direct-local or remote SSH).

Usage: postgresql-matrix-accounting.py WORK_DIR INVENTORY_FILE
Prints JSON that must be identical between a direct-local and a remote run of the same fault mode on
identical clean Host snapshots. Values that legitimately differ per run (Plan ids, attempt ids,
fencing tokens, timestamps) are excluded or normalized.
"""
import json
import re
import sys
from pathlib import Path

work, inventory = Path(sys.argv[1]), Path(sys.argv[2])


def load(path):
    return json.loads((work / path).read_text(encoding="utf-8"))


def result(path):
    return load(path)["observation"]


def norm_backup_id(value):
    return re.sub(r"^[0-9a-f]{16}-", "PLAN-", value)


def backup(observation):
    b = observation["backup"]
    return {
        "backupId": norm_backup_id(b["backupId"]),
        "sourceGeneration": b["sourceGeneration"],
        "destinationClass": b["destinationClass"],
        "destinationReference": b["destinationReference"],
        "path": re.sub(r"/[0-9a-f]{16}-", "/PLAN-", b["path"]),
        "format": b["format"],
        "outsideGeneration": b["outsideGeneration"],
        "schemaVersion": b["schemaVersion"],
    }


def restore(observation):
    r = observation["restore"]
    return {
        "backupId": norm_backup_id(r["backupId"]),
        "candidateGeneration": r["candidateGeneration"]["id"],
        "candidateOwnedResources": r["candidateGeneration"]["ownedResources"],
        "isolated": r["isolated"],
        "verified": r["verified"],
        "schemaVersion": r["schemaVersion"],
    }


def generation(record):
    return {k: record[k] for k in ("id", "logicalId", "role", "authority", "postgresqlVersion", "imageManifest", "ownedResources")}


def outcomes(directory, suffix=""):
    out = {}
    for path in sorted((work / directory).glob(f"op-??{suffix}.json")):
        out[path.name] = json.loads(path.read_text(encoding="utf-8"))["outcome"]
    return out


initial_host = load("initial-final-host.json")["database"]["deployment"]
final_host = load("transition-final-host.json")["database"]["deployment"]
inventory_lines = sorted(
    re.sub(r"/[0-9a-f]{16}-", "/PLAN-", line)
    for line in inventory.read_text(encoding="utf-8").splitlines()
    if line.strip()
)
json.dump(
    {
        "logicalDatabase": final_host["active"]["logicalId"],
        "storeGenerations": {
            "initial": generation(initial_host["active"]),
            "active": generation(final_host["active"]),
            "retained": [generation(r) for r in final_host.get("retained", [])],
        },
        "workloadRecords": {
            "initial": json.loads((work / "initial-final-http-verify.json").read_text(encoding="utf-8"))["databaseBinding"],
            "transition": json.loads((work / "transition-final-http-verify.json").read_text(encoding="utf-8"))["databaseBinding"],
        },
        "backupEvidence": {"initial": backup(result("initial/op-02.json")), "transition": backup(result("transition/op-04-resumed.json"))},
        "restoreVerification": {"initial": restore(result("initial/op-03.json")), "transition": restore(result("transition/op-05-resumed.json"))},
        "authoritySwitch": {k: result("transition/op-06-resumed.json").get(k) for k in ("transitionPhase", "synchronization")},
        "writeFence": result("transition/op-03-resumed.json")["writeFence"],
        "retention": result("transition/op-08-resumed.json")["previous"]["id"],
        "operationOutcomes": {"initial": outcomes("initial"), "transition": outcomes("transition", "-resumed")},
        "ownedResourceInventory": inventory_lines,
    },
    sys.stdout,
    indent=2,
    sort_keys=True,
)
print()
