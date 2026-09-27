#!/usr/bin/env bash
# Destructive direct-local acceptance matrix for the complete asynchronous Host
# tracer. This script must run on the disposable Host Target as its bootstrap
# operator; every Provision operation uses the local Host Handler and the
# restricted executor on the same machine.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME [--scenario complete|worker-rollback]" >&2
  exit 2
}

provision=""
signing_key=""
secret_file=""
work_dir=""
provision_version=""
confirmed_host=""
scenario="complete"
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --scenario) (($# >= 2)) || usage; scenario="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$provision" && -x "$provision" && -f "$provision" && ! -L "$provision" ]] || usage
[[ -n "$signing_key" && -f "$signing_key" && ! -L "$signing_key" ]] || usage
[[ -n "$secret_file" && -f "$secret_file" && ! -L "$secret_file" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$scenario" == complete || "$scenario" == worker-rollback ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local async acceptance requires Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "operator identity is unsupported" >&2; exit 1; }
actual_host="$(hostname)"
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
for path in \
  "$repo_root/examples/host-async/application.yaml" \
  "$repo_root/examples/host-async/environment.yaml" \
  "$repo_root/examples/host-async/revision.yaml" \
  "$repo_root/examples/host-async/revision-worker-v2.yaml" \
  "$repo_root/examples/host-async/revision-task-v2-worker-v2.yaml" \
  "$repo_root/examples/host-async/root.yaml" \
  "$repo_root/examples/host-async/root-worker-v2.yaml" \
  "$repo_root/examples/host-async/root-task-v2-worker-v2.yaml" \
  "$repo_root/scripts/test-first-scheduled-message-host.sh" \
  "$repo_root/scripts/test-worker-handoff-recovery-host.sh" \
  "$repo_root/scripts/test-schedule-recovery-host.sh" \
  "$repo_root/scripts/direct-local-fault-bin/sudo"; do
  [[ -f "$path" && ! -L "$path" ]] || { echo "required acceptance input is missing or unsafe: $path" >&2; exit 1; }
done
[[ -x "$repo_root/scripts/test-first-scheduled-message-host.sh" ]] || { echo "baseline acceptance script is not executable" >&2; exit 1; }
[[ -x "$repo_root/scripts/test-worker-handoff-recovery-host.sh" ]] || { echo "Worker recovery acceptance script is not executable" >&2; exit 1; }
[[ -x "$repo_root/scripts/test-schedule-recovery-host.sh" ]] || { echo "Schedule recovery acceptance script is not executable" >&2; exit 1; }
[[ -x "$repo_root/scripts/direct-local-fault-bin/sudo" ]] || { echo "direct-local fault helper is not executable" >&2; exit 1; }
command -v python3 >/dev/null
command -v sha256sum >/dev/null
sudo -n true

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
secret_file="$(cd "$(dirname "$secret_file")" && pwd)/$(basename "$secret_file")"
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

cp "$repo_root/examples/host-async/application.yaml" "$config_dir/application.yaml"
cp "$repo_root/examples/host-async/revision.yaml" "$config_dir/revision.yaml"
cp "$repo_root/examples/host-async/revision-worker-v2.yaml" "$config_dir/revision-worker-v2.yaml"
cp "$repo_root/examples/host-async/revision-task-v2-worker-v2.yaml" "$config_dir/revision-task-v2-worker-v2.yaml"
cp "$repo_root/examples/host-async/root.yaml" "$config_dir/root.yaml"
cp "$repo_root/examples/host-async/root-worker-v2.yaml" "$config_dir/root-worker-v2.yaml"
cp "$repo_root/examples/host-async/root-task-v2-worker-v2.yaml" "$config_dir/root-task-v2-worker-v2.yaml"
python3 - "$repo_root/examples/host-async/environment.yaml" "$config_dir/environment.yaml" "$operator" <<'PY'
from pathlib import Path
import sys

source = Path(sys.argv[1])
destination = Path(sys.argv[2])
operator = sys.argv[3]
lines = source.read_text(encoding="utf-8").splitlines()
out = []
for line in lines:
    if line.strip().startswith("address:"):
        out.append("    local: true")
    elif line.strip().startswith("user:"):
        out.append(f"    user: {operator}")
    else:
        out.append(line)
destination.write_text("\n".join(out) + "\n", encoding="utf-8")
PY

write_summary() {
  local destination="$1"
  shift
  python3 - "$destination" "$provision" "$provision_version" "$@" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

destination = Path(sys.argv[1])
provision = Path(sys.argv[2])
version = sys.argv[3]
inspection_paths = [Path(path) for path in sys.argv[4:]]
binary_digest = "sha256:" + hashlib.sha256(provision.read_bytes()).hexdigest()
inspections = [json.loads(path.read_text(encoding="utf-8")) for path in inspection_paths]
latest = inspections[-1]
async_status = latest.get("async", {})
capabilities = async_status.get("capabilities", {})
deployment = async_status.get("deployment", {})

def generation(record, kind):
    if not record:
        return None
    return {
        "kind": kind,
        "id": record.get("generationId") or record.get("id"),
        "revision": record.get("revision"),
        "artifactDigest": record.get("artifactDigest"),
        "systemdUnit": record.get("systemdUnit") or record.get("timerUnit"),
        "health": record.get("health"),
        "active": record.get("active"),
        "restartable": record.get("restartable"),
    }

def compact(value):
    if value is None:
        return None
    if isinstance(value, dict):
        return {key: compact(val) for key, val in sorted(value.items()) if val is not None}
    if isinstance(value, list):
        return [compact(item) for item in value]
    return value

queue = deployment.get("queue") or {}
active_generations = compact({
    "queue": generation(queue, "queue"),
    "worker": generation(deployment.get("activeWorker"), "worker"),
    "task": generation(deployment.get("activeTask"), "task"),
    "schedule": generation(deployment.get("schedule"), "schedule"),
})
retained_generations = compact({
    "previousWorker": generation(deployment.get("previousWorker"), "worker"),
    "candidateWorker": generation(deployment.get("candidateWorker"), "worker"),
})
owned_resources = sorted(queue.get("ownedResources") or [])
artifact_versions = sorted({
    value
    for record in (
        queue,
        deployment.get("activeWorker") or {},
        deployment.get("previousWorker") or {},
        deployment.get("candidateWorker") or {},
        deployment.get("activeTask") or {},
        deployment.get("schedule") or {},
    )
    for value in (
        record.get("artifactDigest"),
        record.get("imageManifest"),
        record.get("appletDigest"),
        record.get("ledgerSchema"),
    )
    if value
})

observed_messages = []
observed_occurrences = []
operation_results = []
message_occurrences = {}
for path in sorted(destination.parent.rglob("*.json")):
    if path.name == destination.name:
        continue
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        continue
    if document.get("schemaVersion") != "provision.dev/host-operation-result/v1alpha1":
        continue
    relative = str(path.relative_to(destination.parent))
    operation_results.append({
        "path": relative,
        "operationId": document.get("operationId"),
        "outcome": document.get("outcome"),
        "fencingToken": document.get("fencingToken"),
    })
    observation = document.get("observation") or {}
    message_id = observation.get("messageId")
    if message_id:
        observed_messages.append({
            "path": relative,
            "messageId": message_id,
            "acknowledged": observation.get("acknowledged", "not-reported"),
            "status": observation.get("status"),
            "operationOutcome": document.get("outcome"),
        })
        message_occurrences.setdefault(message_id, []).append(relative)
    occurrence = observation.get("occurrence")
    invocation = observation.get("taskInvocation")
    if occurrence and invocation:
        if occurrence.get("taskInvocationId") != invocation.get("id"):
            raise SystemExit(f"{relative} occurrence does not match Task Invocation")
        if occurrence.get("disposition") != invocation.get("outcome"):
            raise SystemExit(f"{relative} occurrence disposition does not match Task Invocation outcome")
        observed_occurrences.append({
            "path": relative,
            "occurrenceId": occurrence.get("id"),
            "taskInvocationId": invocation.get("id"),
            "taskGenerationId": occurrence.get("taskGenerationId"),
            "disposition": occurrence.get("disposition"),
            "invocationOutcome": invocation.get("outcome"),
        })
if not observed_messages:
    raise SystemExit("matrix summary found no publisher-confirmed message observations")
for message in observed_messages:
    if message.get("acknowledged") is False:
        raise SystemExit(f"message was not acknowledged in matrix evidence: {message!r}")

destination.write_text(json.dumps({
    "schemaVersion": "provision.dev/direct-local-async-matrix-evidence/v1alpha1",
    "provisionVersion": version,
    "provisionDigest": binary_digest,
    "host": {
        "os": latest.get("os"),
        "osVersion": latest.get("osVersion"),
        "architecture": latest.get("architecture"),
        "systemdVersion": latest.get("systemdVersion"),
        "executorDigest": latest.get("executorDigest"),
        "authorityKeyId": latest.get("authorityKeyId"),
    },
    "async": {
        "rabbitmqVersion": capabilities.get("rabbitmqVersion"),
        "rabbitmqImageManifest": capabilities.get("rabbitmqImageManifest"),
        "scheduleAppletDigest": capabilities.get("scheduleAppletDigest"),
        "scheduleLedgerSchema": capabilities.get("scheduleLedgerSchema"),
    },
    "matrix": {
        "scenario": Path(destination).parent.name,
        "baseline": "baseline",
        "workerHealthy": "worker-healthy",
        "workerRollback": "worker-rollback",
        "scheduleRecovery": "schedule-recovery",
    },
    "activeGenerationAccounting": active_generations,
    "retainedGenerationAccounting": retained_generations,
    "ownedResourceInventory": owned_resources,
    "artifactVersions": artifact_versions,
    "operationResults": operation_results,
    "messageAccounting": observed_messages,
    "messageIdentityOccurrences": message_occurrences,
    "occurrenceAccounting": observed_occurrences,
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

assert_clean_bootstrap() {
  python3 - "$work_dir/initial-bootstrap.json" <<'PY'
import json
import sys

inspection = json.load(open(sys.argv[1], encoding="utf-8"))
if not inspection.get("ready") or inspection.get("findings"):
    raise SystemExit(f"bootstrap is not clean and ready: {inspection!r}")
deployment = inspection.get("async", {}).get("deployment", {})
if any(deployment.get(key) for key in ("queue", "activeWorker", "candidateWorker", "previousWorker", "activeTask", "schedule")):
    raise SystemExit(f"direct-local async matrix requires a clean VM snapshot; observed deployment={deployment!r}")
PY
}

seed_task_replacement_artifact() {
  local original_digest="ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5"
  local original="/var/lib/provision/artifacts/sha256/$original_digest"
  local candidate="$work_dir/task-v2-repacked.tar.gz"
  sudo -n test -f "$original"
  sudo -n gzip -cd "$original" | gzip -1 -c >"$candidate"
  local digest
  digest="$(sha256sum "$candidate" | cut -d ' ' -f1)"
  [[ "$digest" =~ ^[0-9a-f]{64}$ && "$digest" != "$original_digest" ]] || {
    echo "failed to create a distinct Task replacement artifact" >&2
    exit 1
  }
  sudo -n install -o root -g root -m 0644 "$candidate" "/var/lib/provision/artifacts/sha256/$digest"
  python3 - "$config_dir/revision-task-v2-worker-v2.yaml" "sha256:$digest" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
replacement = sys.argv[2]
data = path.read_text(encoding="utf-8")
data = data.replace(
    "digest: sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5",
    f"digest: {replacement}",
)
path.write_text(data, encoding="utf-8")
PY
}

echo "[1/5] inspect clean direct-local asynchronous Host capability"
"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/initial-bootstrap.json"
assert_clean_bootstrap
"$provision" config validate --file "$config_dir/root.yaml" >"$work_dir/root-validation.json"
"$provision" config validate --file "$config_dir/root-worker-v2.yaml" >"$work_dir/root-worker-v2-validation.json"

echo "[2/5] establish Queue, Task, Worker, and Schedule baseline"
"$repo_root/scripts/test-first-scheduled-message-host.sh" \
  --provision "$provision" \
  --signing-key "$signing_key" \
  --config "$config_dir/root.yaml" \
  --secret-file "$secret_file" \
  --work-dir "$work_dir/baseline" \
  --local \
  --operator "$operator"

if [[ "$scenario" == worker-rollback ]]; then
  echo "[3/3] prove direct-local Worker rollback from a failed candidate"
  "$repo_root/scripts/test-worker-handoff-recovery-host.sh" \
    --provision "$provision" \
    --signing-key "$signing_key" \
    --config "$config_dir/root-worker-v2.yaml" \
    --secret-file "$secret_file" \
    --state-seed "$work_dir/baseline/state.db" \
    --work-dir "$work_dir/worker-rollback" \
    --local \
    --operator "$operator" \
    --scenario rollback

  "$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/final-bootstrap.json"
  write_summary "$work_dir/matrix-summary.json" \
    "$work_dir/initial-bootstrap.json" \
    "$work_dir/baseline/bootstrap-after.json" \
    "$work_dir/worker-rollback/bootstrap-after.json" \
    "$work_dir/final-bootstrap.json"

  echo "direct-local asynchronous Worker rollback matrix passed"
  echo "evidence: $work_dir"
  exit 0
fi

echo "[3/4] prove direct-local Worker healthy handoff and bounded recovery"
"$repo_root/scripts/test-worker-handoff-recovery-host.sh" \
  --provision "$provision" \
  --signing-key "$signing_key" \
  --config "$config_dir/root-worker-v2.yaml" \
  --secret-file "$secret_file" \
  --state-seed "$work_dir/baseline/state.db" \
  --work-dir "$work_dir/worker-healthy" \
  --local \
  --operator "$operator" \
  --scenario healthy

echo "[4/4] prove direct-local Schedule handoff, interruption recovery, and runtime restart"
seed_task_replacement_artifact
"$provision" config validate --file "$config_dir/root-task-v2-worker-v2.yaml" >"$work_dir/root-task-v2-worker-v2-validation.json"
"$repo_root/scripts/test-schedule-recovery-host.sh" \
  --provision "$provision" \
  --signing-key "$signing_key" \
  --config "$config_dir/root-task-v2-worker-v2.yaml" \
  --secret-file "$secret_file" \
  --state-seed "$work_dir/worker-healthy/state.db" \
  --work-dir "$work_dir/schedule-recovery" \
  --local \
  --operator "$operator"

"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/final-bootstrap.json"
write_summary "$work_dir/matrix-summary.json" \
  "$work_dir/initial-bootstrap.json" \
  "$work_dir/baseline/bootstrap-after.json" \
  "$work_dir/worker-healthy/bootstrap-after.json" \
  "$work_dir/schedule-recovery/bootstrap-after-runtime-restart.json" \
  "$work_dir/final-bootstrap.json"

echo "direct-local asynchronous failure matrix passed"
echo "evidence: $work_dir"
