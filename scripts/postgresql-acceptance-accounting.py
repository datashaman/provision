#!/usr/bin/env python3
"""Print the transport-independent PostgreSQL acceptance accounting as JSON.

Shared by the direct-local and remote-SSH matrices so the parity runner can
compare exactly what each run observed, excluding per-run identifiers such as
attempt ids, fencing tokens, timestamps, and backup ids.
"""
import json
import sys
from pathlib import Path

run_dir, final_host, final_http = (Path(p) for p in sys.argv[1:4])


def load(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def generation(record):
    keys = ("id", "logicalId", "role", "authority", "ready", "postgresqlVersion", "imageManifest",
            "serviceUnit", "container", "account", "dataPath", "database", "connectivity", "health",
            "supportedGuarantees", "ownedResources")
    return {key: record.get(key) for key in keys}


host = load(final_host)
deployment = host["database"]["deployment"]
capabilities = host["database"]["capabilities"]
transition = run_dir / "transition"
ops = {path.stem: load(path) for path in sorted(transition.glob("op-*-resumed.json"))}
backup = ops["op-04-resumed"]["observation"]["backup"]
restore = ops["op-05-resumed"]["observation"]["restore"]

journal = {}
for path in sorted(transition.glob("op-*-resumed-status.json")):
    operation = path.name.removesuffix("-resumed-status.json")
    events = [e for e in load(path)["events"] if e["operationId"] == operation]
    journal[operation] = [[e["kind"], e.get("outcome", "-"), bool(e.get("observation", {}).get("resumeOfAttemptId"))] for e in events]

initial = load(run_dir / "initial-final-host.json")["database"]["deployment"]["active"]
print(json.dumps({
    "initialGeneration": generation(initial),
    "initialWorkloadRecords": load(run_dir / "initial-final-http-verify.json")["databaseBinding"],
    "logicalDatabase": deployment["active"]["logicalId"],
    "activeGeneration": generation(deployment["active"]),
    "retainedGenerations": [generation(item) for item in deployment.get("retained", [])],
    "guarantees": {key: capabilities.get(key) for key in ("storeRollbackGuarantee", "forwardCutoverGuarantee", "supportedTransitionMechanism")},
    "workloadRecords": load(final_http)["databaseBinding"],
    "backup": {key: backup.get(key) for key in ("schemaVersion", "sourceGeneration", "destinationClass", "destinationReference", "format", "outsideGeneration")},
    "restoreVerification": {
        "schemaVersion": restore.get("schemaVersion"),
        "candidateGeneration": generation(restore["candidateGeneration"]),
        "isolated": restore.get("isolated"),
        "verified": restore.get("verified"),
    },
    "transitionPhases": {name: ops[name]["observation"].get("transitionPhase") for name in sorted(ops)},
    "writeFence": ops["op-03-resumed"]["observation"].get("writeFence"),
    "retainedPrevious": ops["op-08-resumed"]["observation"]["previous"]["id"],
    "journal": journal,
}, indent=2, sort_keys=True))
