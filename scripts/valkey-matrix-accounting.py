#!/usr/bin/env python3
"""Normalized logical accounting of one direct-local Valkey failure-matrix run.

Usage: valkey-matrix-accounting.py WORK_DIR INVENTORY_FILE
Plan ids, attempt ids, fencing tokens and timestamps are excluded, so runs of different
fault modes on identical clean snapshots are comparable.
"""
import json
import re
import sys
from pathlib import Path

work, inventory = Path(sys.argv[1]), Path(sys.argv[2])
load = lambda p: json.loads((work / p).read_text(encoding="utf-8"))
gen_keys = ("id", "logicalId", "valkeyVersion", "imageManifest", "appendOnly", "appendFsync", "port", "health")
generation = lambda g: {k: g.get(k) for k in gen_keys}
keys = lambda p: sorted((work / p).read_text(encoding="utf-8").splitlines())
outcomes = {
    path.name: json.loads(path.read_text(encoding="utf-8"))["outcome"]
    for path in sorted((work / "transition").glob("op-??-resumed.json"))
}
fault_record = {
    path.name.removesuffix("-fault.json"): json.loads(path.read_text(encoding="utf-8"))
    for path in sorted((work / "transition").glob("op-??-fault.json"))
}
uncertain = {
    path.name.removesuffix("-uncertain.json"): json.loads(path.read_text(encoding="utf-8"))
    for path in sorted((work / "transition").glob("op-??-uncertain.json"))
}
final = load("transition-final-host.json")["keyValue"]["active"]
fence = load("transition/op-03-resumed.json")["observation"]
switch = load("transition/op-04-resumed.json")["observation"]
retain = load("transition/op-06-resumed.json")["observation"]
json.dump(
    {
        "logicalStore": final["logicalId"],
        "storeGenerations": {
            "initial": generation(load("initial-final-host.json")["keyValue"]["active"]),
            "active": generation(final),
            "retained": retain["previous"]["id"],
        },
        "workloadKeys": {"initial": keys("initial-keys.txt"), "transition": keys("transition-keys.txt")},
        "writeFence": fence["writeFence"],
        "replicationAtFence": fence.get("replication"),
        "authoritySwitch": {k: switch.get(k) for k in ("transitionPhase", "synchronization")},
        "retention": {k: retain.get(k) for k in ("synchronization", "storeRollbackGuarantee", "transitionCleanupPolicy")},
        "operationOutcomes": outcomes,
        "faultRecord": fault_record,
        "uncertainOutcomes": uncertain,
        "ownedResourceInventory": sorted(
            line for line in inventory.read_text(encoding="utf-8").splitlines() if line.strip()
        ),
    },
    sys.stdout,
    indent=2,
    sort_keys=True,
)
print()
