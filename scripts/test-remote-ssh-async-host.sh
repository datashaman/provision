#!/usr/bin/env bash
# Destructive remote-SSH acceptance matrix for the complete asynchronous Host
# tracer. Provision, approval state, and the signing key remain on the
# controller; the disposable Host Target receives only strict-SSH observations
# and signed typed operations for the restricted executor.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --work-dir EMPTY_PATH --provision-version VERSION --target ADDRESS --target-user USER --confirm-disposable-host HOSTNAME [--scenario complete|worker-rollback] [--incus-host HOST --incus-instance NAME]" >&2
  exit 2
}

provision=""
signing_key=""
secret_file=""
work_dir=""
provision_version=""
target_address=""
target_user=""
confirmed_host=""
scenario="complete"
incus_host=""
incus_instance=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target_address="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --scenario) (($# >= 2)) || usage; scenario="$2"; shift 2 ;;
    --incus-host) (($# >= 2)) || usage; incus_host="$2"; shift 2 ;;
    --incus-instance) (($# >= 2)) || usage; incus_instance="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$provision" && -x "$provision" && -f "$provision" && ! -L "$provision" ]] || usage
[[ -n "$signing_key" && -f "$signing_key" && ! -L "$signing_key" ]] || usage
[[ -n "$secret_file" && -f "$secret_file" && ! -L "$secret_file" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" ]] || usage
[[ "$target_address" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || usage
[[ "$target_user" =~ ^[a-z_][a-z0-9_-]*$ && -n "$confirmed_host" ]] || usage
[[ "$scenario" == complete || "$scenario" == worker-rollback ]] || usage
if [[ -n "$incus_host" || -n "$incus_instance" ]]; then
  [[ "$incus_host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$incus_instance" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || usage
fi

actor="$(id -un)"
[[ "$actor" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "controller identity is unsupported" >&2; exit 1; }
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fault_bin="$repo_root/scripts/remote-ssh-fault-bin"
for path in \
  "$repo_root/examples/host-async/application.yaml" \
  "$repo_root/examples/host-async/environment.yaml" \
  "$repo_root/examples/host-async/revision.yaml" \
  "$repo_root/examples/host-async/revision-worker-v2.yaml" \
  "$repo_root/examples/host-async/revision-task-v2-worker-v2.yaml" \
  "$repo_root/examples/host-async/root.yaml" \
  "$repo_root/examples/host-async/root-worker-v2.yaml" \
  "$repo_root/examples/host-async/root-task-v2-worker-v2.yaml" \
  "$repo_root/scripts/test-worker-handoff-recovery-host.sh" \
  "$repo_root/scripts/test-schedule-recovery-host.sh" \
  "$fault_bin/ssh"; do
  [[ -f "$path" && ! -L "$path" ]] || { echo "required acceptance input is missing or unsafe: $path" >&2; exit 1; }
done
[[ -x "$repo_root/scripts/test-worker-handoff-recovery-host.sh" ]] || { echo "Worker recovery acceptance script is not executable" >&2; exit 1; }
[[ -x "$repo_root/scripts/test-schedule-recovery-host.sh" ]] || { echo "Schedule recovery acceptance script is not executable" >&2; exit 1; }
[[ -x "$fault_bin/ssh" ]] || { echo "remote SSH fault helper is not executable" >&2; exit 1; }
for command in python3 ssh ssh-keygen uname; do command -v "$command" >/dev/null; done
real_ssh="$(command -v ssh)"
ssh_options=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
secret_file="$(cd "$(dirname "$secret_file")" && pwd)/$(basename "$secret_file")"
state="$work_dir/state.db"
secret_reference="secret://lab/rabbitmq-url"
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

cp "$repo_root/examples/host-async/application.yaml" "$config_dir/application.yaml"
cp "$repo_root/examples/host-async/revision.yaml" "$config_dir/revision.yaml"
cp "$repo_root/examples/host-async/revision-worker-v2.yaml" "$config_dir/revision-worker-v2.yaml"
cp "$repo_root/examples/host-async/revision-task-v2-worker-v2.yaml" "$config_dir/revision-task-v2-worker-v2.yaml"
cp "$repo_root/examples/host-async/root.yaml" "$config_dir/root.yaml"
cp "$repo_root/examples/host-async/root-worker-v2.yaml" "$config_dir/root-worker-v2.yaml"
cp "$repo_root/examples/host-async/root-task-v2-worker-v2.yaml" "$config_dir/root-task-v2-worker-v2.yaml"
python3 - "$repo_root/examples/host-async/environment.yaml" "$config_dir/environment.yaml" "$target_address" "$target_user" <<'PY'
from pathlib import Path
import sys

source = Path(sys.argv[1])
destination = Path(sys.argv[2])
address = sys.argv[3]
user = sys.argv[4]
lines = source.read_text(encoding="utf-8").splitlines()
out = []
for line in lines:
    if line.strip().startswith("address:"):
        out.append(f"    address: {address}")
    elif line.strip().startswith("user:"):
        out.append(f"    user: {user}")
    else:
        out.append(line)
destination.write_text("\n".join(out) + "\n", encoding="utf-8")
PY

remote() {
  "$real_ssh" "${ssh_options[@]}" "$target_user@$target_address" "$1"
}

inspect_host() {
  "$provision" host bootstrap check --address "$target_address" --user "$target_user" --environment lab --operator "$target_user"
}

plan_id() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' < "$1"
}

expire_test_lease() {
  python3 - "$state" <<'PY'
import sqlite3, sys
connection = sqlite3.connect(sys.argv[1])
connection.execute("UPDATE execution_leases SET expires_at = '2000-01-01T00:00:00Z' WHERE released_at IS NULL")
connection.commit()
connection.close()
PY
}

status() {
  "$provision" deployment status --plan "$plan" --state "$state" >"$1"
}

execute_operation() {
  local operation="$1"
  local args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || args+=(--secret-file "$secret_reference=$secret_file")
  "$provision" "${args[@]}" >"$work_dir/baseline/$operation.json"
}

assert_interrupted_intent() {
  python3 - "$1" "$2" <<'PY'
import json, sys
events = [e for e in json.load(open(sys.argv[1], encoding="utf-8"))["events"] if e["operationId"] == sys.argv[2]]
if not events or events[-1]["kind"] != "intent":
    raise SystemExit(f"expected latest {sys.argv[2]} event to be an interrupted intent: {events!r}")
PY
}

interrupt_before_and_after() {
  local operation="$1"
  local before_marker="$work_dir/baseline/$operation.before-dispatch"
  local after_marker="$work_dir/baseline/$operation.after-host-completion"
  local args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || args+=(--secret-file "$secret_reference=$secret_file")

  set +e
  PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=before PROVISION_FAULT_MARKER="$before_marker" \
    "$provision" "${args[@]}" >"$work_dir/baseline/$operation.before.txt" 2>&1
  local before_status=$?
  set -e
  [[ $before_status -ne 0 && -f "$before_marker" ]] || { echo "$operation was not interrupted before Host dispatch" >&2; exit 1; }
  status "$work_dir/baseline/$operation.before-status.json"
  assert_interrupted_intent "$work_dir/baseline/$operation.before-status.json" "$operation"
  expire_test_lease

  local resume_args=(deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || resume_args+=(--secret-file "$secret_reference=$secret_file")
  set +e
  PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
    "$provision" "${resume_args[@]}" >"$work_dir/baseline/$operation.after.txt" 2>&1
  local after_status=$?
  set -e
  [[ $after_status -ne 0 && -f "$after_marker" ]] || { echo "$operation was not interrupted after Host completion" >&2; exit 1; }
  status "$work_dir/baseline/$operation.after-status.json"
  assert_interrupted_intent "$work_dir/baseline/$operation.after-status.json" "$operation"
  expire_test_lease

  "$provision" "${resume_args[@]}" >"$work_dir/baseline/$operation.resumed.json"
  python3 - "$work_dir/baseline/$operation.resumed.json" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
if result.get("outcome") != "succeeded":
    raise SystemExit(f"resumed operation did not succeed: {result!r}")
PY
}

interrupt_after_completion() {
  local operation="$1"
  local after_marker="$work_dir/baseline/$operation.after-host-completion"
  set +e
  PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
    "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/baseline/$operation.after.txt" 2>&1
  local after_status=$?
  set -e
  [[ $after_status -ne 0 && -f "$after_marker" ]] || { echo "$operation was not interrupted after Host completion" >&2; exit 1; }
  status "$work_dir/baseline/$operation.after-status.json"
  assert_interrupted_intent "$work_dir/baseline/$operation.after-status.json" "$operation"
  expire_test_lease

  "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/baseline/$operation.resumed.json"
  python3 - "$work_dir/baseline/$operation.resumed.json" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
if result.get("outcome") != "succeeded":
    raise SystemExit(f"resumed operation did not succeed: {result!r}")
observation = result.get("observation") or {}
if observation.get("status") != "verified" or not observation.get("messageId"):
    raise SystemExit(f"resumed Schedule verification omitted Task delivery evidence: {result!r}")
PY
}

assert_clean_bootstrap() {
  python3 - "$work_dir/initial-bootstrap.json" "$confirmed_host" <<'PY'
import json
import sys

inspection = json.load(open(sys.argv[1], encoding="utf-8"))
confirmed_host = sys.argv[2]
if not inspection.get("ready") or inspection.get("findings"):
    raise SystemExit(f"bootstrap is not clean and ready: {inspection!r}")
if confirmed_host and inspection.get("environment") != "lab":
    raise SystemExit(f"unexpected environment in Host inspection: {inspection!r}")
deployment = inspection.get("async", {}).get("deployment", {})
if any(deployment.get(key) for key in ("queue", "activeWorker", "candidateWorker", "previousWorker", "activeTask", "schedule")):
    raise SystemExit(f"remote async matrix requires a clean VM snapshot; observed deployment={deployment!r}")
PY
  remote_host="$(remote 'hostname')"
  [[ "$confirmed_host" == "$remote_host" ]] || { echo "disposable-host confirmation does not match $remote_host" >&2; exit 1; }
}

assert_baseline() {
  "$provision" deployment status --plan "$plan" --state "$state" >"$work_dir/baseline/deployment-status.json"
  inspect_host > "$work_dir/baseline/bootstrap-after.json"
  python3 - "$work_dir/baseline/op-14.resumed.json" "$work_dir/baseline/bootstrap-after.json" <<'PY'
import json
import sys

operation = json.load(open(sys.argv[1], encoding="utf-8"))
observed = operation["observation"]
if operation["outcome"] != "succeeded" or observed["status"] != "verified":
    raise SystemExit("Schedule verification did not succeed")
if not observed.get("messageId") or not observed.get("acknowledged"):
    raise SystemExit("confirmed message was not joined to Worker acknowledgement")
invocation = observed["taskInvocation"]
occurrence = observed["occurrence"]
if invocation["id"] != occurrence["taskInvocationId"] or invocation["trigger"] != "schedule":
    raise SystemExit("occurrence and Task Invocation identities do not match")
if invocation["outcome"] != "succeeded" or len(invocation["attempts"]) != 1:
    raise SystemExit("Task Invocation attempt history is incomplete")

inspection = json.load(open(sys.argv[2], encoding="utf-8"))
status = inspection.get("async", {}).get("deployment", {})
worker = status.get("activeWorker")
if worker is None:
    raise SystemExit(f"post-run Host inspection omitted activeWorker; findings={inspection.get('findings', [])!r}; asyncFindings={inspection.get('async', {}).get('findings', [])!r}")
if worker["gate"] != "open" or not worker["queueConnected"]:
    raise SystemExit("active Worker is not open and Queue-connected")
if status["activeTask"]["id"] != invocation["taskGenerationId"]:
    raise SystemExit("active Task does not match the invocation")
if not status["schedule"]["active"] or status["schedule"]["taskGenerationId"] != status["activeTask"]["id"]:
    raise SystemExit("stable Schedule does not target the active Task generation")
if len(status.get("occurrences", [])) != 1 or len(status.get("taskInvocations", [])) != 1:
    raise SystemExit("structured status does not distinguish one occurrence and Task Invocation")
PY
}

assert_secret_absent() {
  local password
  password="$(sed -E 's#^[^:]+://[^:]+:([^@]+)@.*#\1#' "$secret_file")"
  [[ -n "$password" ]] || { echo "resolved Queue secret has no password" >&2; exit 1; }
  if grep -R -F -- "$password" "$work_dir" >/dev/null 2>&1; then
    echo "resolved Queue credential leaked into durable acceptance output" >&2
    exit 1
  fi
}

seed_task_replacement_artifact() {
  local original_digest="ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5"
  local digest
  digest="$(remote "set -eu; original=/var/lib/provision/artifacts/sha256/$original_digest; candidate=/tmp/provision-async-task-v2-repacked.tar.gz; sudo -n test -f \"\$original\"; sudo -n gzip -cd \"\$original\" | gzip -1 -c >\"\$candidate\"; digest=\$(sha256sum \"\$candidate\" | cut -d ' ' -f1); test \"\$digest\" != '$original_digest'; sudo -n install -o root -g root -m 0644 \"\$candidate\" \"/var/lib/provision/artifacts/sha256/\$digest\"; printf '%s\n' \"\$digest\"")"
  [[ "$digest" =~ ^[0-9a-f]{64}$ && "$digest" != "$original_digest" ]] || {
    echo "failed to create a distinct remote Task replacement artifact" >&2
    exit 1
  }
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

write_summary() {
  local destination="$1"
  shift
  python3 - "$destination" "$provision" "$provision_version" "$target_address" "$target_user" "$@" <<'PY'
import hashlib
import json
import platform
import subprocess
from pathlib import Path
import sys

destination = Path(sys.argv[1])
provision = Path(sys.argv[2])
version = sys.argv[3]
target = sys.argv[4]
target_user = sys.argv[5]
inspection_paths = [Path(path) for path in sys.argv[6:]]
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

known_hosts = subprocess.run(["ssh-keygen", "-F", target], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
known_hosts_entries = [line for line in known_hosts.stdout.splitlines() if line and not line.startswith("#")]

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
    "schemaVersion": "provision.dev/remote-ssh-async-matrix-evidence/v1alpha1",
    "provisionVersion": version,
    "provisionDigest": binary_digest,
    "controller": {
        "os": platform.system(),
        "osVersion": platform.release(),
        "architecture": platform.machine(),
        "user": platform.node(),
    },
    "ssh": {
        "target": target,
        "targetUser": target_user,
        "strictHostKeyChecking": True,
        "passwordAuthentication": False,
        "hostFingerprint": latest.get("sshHostKeyFingerprint"),
        "knownHostsEntries": known_hosts_entries,
    },
    "host": {
        "os": latest.get("os"),
        "osVersion": latest.get("osVersion"),
        "architecture": latest.get("architecture"),
        "systemdVersion": latest.get("systemdVersion"),
        "sshServerVersion": latest.get("sshServerVersion"),
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
        "queuePreparationTransportLoss": "op-01 before dispatch and after Host completion",
        "taskDeliveryTransportLoss": "op-14 after Host completion",
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

echo "[1/5] inspect clean remote asynchronous Host capability"
inspect_host >"$work_dir/initial-bootstrap.json"
assert_clean_bootstrap
"$provision" config validate --file "$config_dir/root.yaml" >"$work_dir/root-validation.json"
"$provision" config validate --file "$config_dir/root-worker-v2.yaml" >"$work_dir/root-worker-v2-validation.json"

echo "[2/5] establish Queue, Task, Worker, and Schedule baseline with SSH response loss"
mkdir -m 0700 "$work_dir/baseline"
"$provision" plan preview --file "$config_dir/root.yaml" --state "$state" > "$work_dir/baseline/plan.json"
plan="$(plan_id "$work_dir/baseline/plan.json")"
"$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan" --actor "$actor" --state "$state" > "$work_dir/baseline/approval.json"
interrupt_before_and_after op-01
for operation in op-02 op-03 op-04 op-04-verify op-05 op-06 op-07 op-10 op-11 op-12 op-13; do
  execute_operation "$operation"
done
interrupt_after_completion op-14
assert_baseline
assert_secret_absent

if [[ "$scenario" == worker-rollback ]]; then
  echo "[3/3] prove remote Worker rollback from a failed candidate"
  "$repo_root/scripts/test-worker-handoff-recovery-host.sh" \
    --provision "$provision" \
    --signing-key "$signing_key" \
    --config "$config_dir/root-worker-v2.yaml" \
    --secret-file "$secret_file" \
    --state-seed "$state" \
    --work-dir "$work_dir/worker-rollback" \
    --target "$target_address" \
    --target-user "$target_user" \
    --scenario rollback

  inspect_host >"$work_dir/final-bootstrap.json"
  write_summary "$work_dir/matrix-summary.json" \
    "$work_dir/initial-bootstrap.json" \
    "$work_dir/baseline/bootstrap-after.json" \
    "$work_dir/worker-rollback/bootstrap-after.json" \
    "$work_dir/final-bootstrap.json"

  echo "remote SSH asynchronous Worker rollback matrix passed"
  echo "evidence: $work_dir"
  exit 0
fi

echo "[3/4] prove remote Worker healthy handoff and bounded recovery"
"$repo_root/scripts/test-worker-handoff-recovery-host.sh" \
  --provision "$provision" \
  --signing-key "$signing_key" \
  --config "$config_dir/root-worker-v2.yaml" \
  --secret-file "$secret_file" \
  --state-seed "$state" \
  --work-dir "$work_dir/worker-healthy" \
  --target "$target_address" \
  --target-user "$target_user" \
  --scenario healthy

echo "[4/4] prove remote Schedule handoff, interruption recovery, and Host reboot"
seed_task_replacement_artifact
"$provision" config validate --file "$config_dir/root-task-v2-worker-v2.yaml" >"$work_dir/root-task-v2-worker-v2-validation.json"
schedule_args=(
  --provision "$provision"
  --signing-key "$signing_key"
  --config "$config_dir/root-task-v2-worker-v2.yaml"
  --secret-file "$secret_file"
  --state-seed "$work_dir/worker-healthy/state.db"
  --work-dir "$work_dir/schedule-recovery"
  --target "$target_address"
  --target-user "$target_user"
)
if [[ -n "$incus_host" ]]; then
  schedule_args+=(--incus-host "$incus_host" --incus-instance "$incus_instance")
fi
"$repo_root/scripts/test-schedule-recovery-host.sh" "${schedule_args[@]}"

inspect_host >"$work_dir/final-bootstrap.json"
write_summary "$work_dir/matrix-summary.json" \
  "$work_dir/initial-bootstrap.json" \
  "$work_dir/baseline/bootstrap-after.json" \
  "$work_dir/worker-healthy/bootstrap-after.json" \
  "$work_dir/schedule-recovery/bootstrap-after-runtime-restart.json" \
  "$work_dir/final-bootstrap.json"

echo "remote SSH asynchronous failure matrix passed"
echo "evidence: $work_dir"
