#!/usr/bin/env bash
# Black-box acceptance for rejecting a failed, gated Worker candidate.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --state-seed FILE --work-dir DIRECTORY --target HOST --target-user USER" >&2
  exit 2
}

provision=""
signing_key=""
config=""
secret_file=""
state_seed=""
work_dir=""
target=""
target_user=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --config) (($# >= 2)) || usage; config="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --state-seed) (($# >= 2)) || usage; state_seed="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -x "$provision" && -f "$signing_key" && -f "$config" && -f "$secret_file" && -f "$state_seed" ]] || usage
[[ "$work_dir" == /* && ! -e "$work_dir" ]] || usage
[[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage

mkdir -m 0700 "$work_dir"
state="$work_dir/state.db"
cp -- "$state_seed" "$state"
chmod 0600 "$state"
secret_reference="secret://lab/rabbitmq-url"

plan_id() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' < "$1"
}

inspect_host() {
  "$provision" host bootstrap check \
    --address "$target" --user "$target_user" --environment lab --operator "$target_user"
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

echo "[1/7] inspect the active asynchronous deployment"
inspect_host > "$work_dir/bootstrap-before.json"
"$provision" config validate --file "$config" > "$work_dir/configuration.json"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/configuration.json" <<'PY'
import json, sys
status = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
configuration = json.load(open(sys.argv[2], encoding="utf-8"))
if not status.get("activeWorker") or status["activeWorker"]["gate"] != "open":
    raise SystemExit("baseline has no exact open active Worker")
if not status.get("queue", {}).get("ready"):
    raise SystemExit("baseline Queue is not ready")
if not status.get("activeTask") or not status.get("schedule", {}).get("active"):
    raise SystemExit("baseline Task or Schedule is not active")
candidate = status.get("candidateWorker")
if candidate:
    workers = [name for name, component in configuration["application"]["components"].items() if component["role"] == "worker"]
    if len(workers) != 1:
        raise SystemExit(f"configuration does not have one Worker component: {workers!r}")
    worker = workers[0]
    revision = configuration["revision"]
    artifact = revision["artifacts"][worker]
    digest_id = artifact["digest"].removeprefix("sha256:")[:12]
    expected = {
        "id": f'{revision["name"]}-{digest_id}',
        "revision": revision["name"],
        "artifactDigest": artifact["digest"],
        "systemdUnit": f'provision-{configuration["environment"]["name"]}-{worker}-{digest_id}.service',
    }
    mismatched = {key: (candidate.get(key), value) for key, value in expected.items() if candidate.get(key) != value}
    if mismatched or candidate.get("active") or candidate.get("gate") != "closed" or candidate.get("inFlight") != 0:
        raise SystemExit(f"baseline has a conflicting Worker candidate: mismatched={mismatched!r} status={candidate!r}")
    if candidate.get("unitActive") != candidate.get("queueConnected"):
        raise SystemExit(f"resumable Worker candidate has inconsistent runtime state: {candidate!r}")
PY

echo "[2/7] preview and approve the Worker-only replacement Plan"
configured_target="$(python3 - "$work_dir/configuration.json" <<'PY'
import json, sys
configuration = json.load(open(sys.argv[1], encoding="utf-8"))
targets = configuration.get("environment", {}).get("targets", {})
addresses = sorted({value.get("address") for value in targets.values() if value.get("kind") == "host" and value.get("address")})
print("\n".join(addresses))
PY
)"
if [[ "$configured_target" != "$target" ]]; then
  printf 'configuration Host Target does not match acceptance target: configured=%q expected=%q\n' "$configured_target" "$target" >&2
  exit 1
fi
"$provision" plan preview --file "$config" --state "$state" > "$work_dir/plan.json"
plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$plan" --actor "$(id -un)" --state "$state" > "$work_dir/approval.json"
candidate_unit="$(python3 - "$work_dir/plan.json" <<'PY'
import json, sys
plan = json.load(open(sys.argv[1], encoding="utf-8"))
kinds = [operation["kind"] for operation in plan["operations"]]
expected = ["prepareQueue", "stageArtifact", "installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate"]
if kinds != expected:
    raise SystemExit(f"unexpected Worker-only Plan operations: {kinds!r}")
if any(operation["kind"] in {"installTaskGeneration", "handoffSchedule"} for operation in plan["operations"]):
    raise SystemExit("Worker-only Plan contains a Task or Schedule transition")
if any(operation["kind"] in {"fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake", "verifyWorkerActive", "retainWorkerPrevious"} for operation in plan["operations"]):
    raise SystemExit("candidate-only Plan contains an unimplemented Worker handoff mutation")
start = next(operation for operation in plan["operations"] if operation["kind"] == "startWorkerCandidate")
print(start["input"]["async"]["worker"]["systemdUnit"])
PY
)"

echo "[3/7] install and start the second Worker Generation with intake disabled"
for operation in op-01 op-02 op-05 op-06; do
  args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || args+=(--secret-file "$secret_reference=$secret_file")
  "$provision" "${args[@]}" > "$work_dir/$operation.json"
done
inspect_host > "$work_dir/bootstrap-candidate-gated.json"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-candidate-gated.json" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
gated = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
candidate = gated.get("candidateWorker")
if not candidate or candidate["gate"] != "closed" or not candidate["unitActive"] or not candidate["queueConnected"] or candidate["inFlight"] != 0:
    raise SystemExit(f"candidate is not verifiably gated: {candidate!r}")
if candidate.get("health") != "candidate-gated":
    raise SystemExit(f"candidate health is not gated: {candidate!r}")
if gated["activeWorker"]["id"] != before["activeWorker"]["id"] or gated["activeWorker"]["gate"] != "open":
    raise SystemExit("starting the candidate displaced or fenced the active Worker")
if gated["queue"]["generationId"] != before["queue"]["generationId"]:
    raise SystemExit("Worker candidate preparation changed the Queue Generation")
PY

echo "[4/7] prove messages remain handled by the active Worker while the candidate is gated"
deadline=$((SECONDS + 90))
while ((SECONDS < deadline)); do
  inspect_host > "$work_dir/bootstrap-message.json"
  if python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-message.json" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
current = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
candidate = current.get("candidateWorker")
before_ids = {message["id"] for message in before.get("messages", [])}
new_messages = [message for message in current.get("messages", []) if message["id"] not in before_ids]
worker_events = [event for message in new_messages for event in message.get("workerEvents", [])]
ok = (
    any(message["disposition"] == "acknowledged" and message.get("workerArtifactDigest") == before["activeWorker"]["artifactDigest"] for message in new_messages) and
    all(event.get("workerArtifactDigest") == before["activeWorker"]["artifactDigest"] for event in worker_events) and
    all(message.get("workerArtifactDigest") != candidate["artifactDigest"] for message in new_messages if candidate) and
    current["activeWorker"]["id"] == before["activeWorker"]["id"] and
    current["activeWorker"]["gate"] == "open" and
    candidate and candidate["gate"] == "closed" and candidate["inFlight"] == 0
)
raise SystemExit(0 if ok else 1)
PY
  then
    break
  fi
  sleep 5
done
((SECONDS < deadline)) || { echo "no active-Worker acknowledgement was observed while the candidate remained gated" >&2; exit 1; }

echo "[5/7] inject candidate liveness failure and reject verification"
if ! ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" "sudo -n systemctl stop '$candidate_unit'"; then
  echo "candidate fault injection requires passwordless sudo for the exact systemctl stop command on the disposable acceptance Host" >&2
  exit 1
fi
if "$provision" deployment execute --plan "$plan" --operation op-07 --state "$state" --signing-key "$signing_key" --lease-duration 2m > "$work_dir/op-07-failed.json" 2> "$work_dir/op-07-failed.stderr"; then
  echo "failed Worker candidate unexpectedly passed verification" >&2
  exit 1
fi
for operation in op-08 op-09 op-10 op-11 op-15; do
  if "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m > "$work_dir/$operation-blocked.stdout" 2> "$work_dir/$operation-blocked.stderr"; then
    echo "downstream Worker operation $operation became executable after failed verification" >&2
    exit 1
  fi
  grep -Fq 'operation is not present in the approved Plan' "$work_dir/$operation-blocked.stderr" || {
    echo "unapproved Worker handoff operation $operation was not rejected by exact-Plan authorization" >&2
    exit 1
  }
done

echo "[6/7] inspect unchanged active identities and actionable failed-candidate status"
"$provision" deployment status --plan "$plan" --state "$state" > "$work_dir/deployment-status.json"
inspect_host > "$work_dir/bootstrap-after.json"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-message.json" "$work_dir/bootstrap-after.json" "$work_dir/op-07-failed.json" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
during = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
after = json.load(open(sys.argv[3], encoding="utf-8"))["async"]["deployment"]
failure = json.load(open(sys.argv[4], encoding="utf-8"))
for key in ("id", "generationId", "imageManifest", "serviceUnit", "container", "dataPath"):
    if after["queue"].get(key) != before["queue"].get(key):
        raise SystemExit(f"Queue identity changed at {key}")
for key in ("id", "revision", "artifactDigest", "systemdUnit"):
    if after["activeWorker"].get(key) != before["activeWorker"].get(key):
        raise SystemExit(f"active Worker changed at {key}")
if after["activeWorker"]["gate"] != "open" or not after["activeWorker"]["unitActive"] or not after["activeWorker"]["queueConnected"]:
    raise SystemExit("active Worker was damaged by failed candidate verification")
if after.get("previousWorker") != before.get("previousWorker"):
    raise SystemExit("retained previous Worker state changed")
if after["activeTask"]["id"] != before["activeTask"]["id"] or after["schedule"]["taskGenerationId"] != before["schedule"]["taskGenerationId"]:
    raise SystemExit("Worker-only attempt changed the Task or Schedule binding")
candidate = after.get("candidateWorker")
if not candidate or candidate["id"] == after["activeWorker"]["id"] or candidate["gate"] != "closed" or candidate["unitActive"]:
    raise SystemExit(f"failed candidate is not separately retained and closed: {candidate!r}")
if candidate.get("health") != "candidate-unverified" or not candidate.get("reason") or not candidate.get("recoveryAction"):
    raise SystemExit(f"failed candidate status is not actionable: {candidate!r}")
checks = failure["observation"].get("checks", {})
if failure["outcome"] != "failed" or checks.get("liveness") or not failure["observation"].get("reason"):
    raise SystemExit(f"failed verification evidence is incomplete: {failure!r}")
before_ids = {message["id"] for message in before.get("messages", [])}
new_messages = [message for message in during.get("messages", []) if message["id"] not in before_ids]
if not any(message["disposition"] == "acknowledged" and message.get("workerArtifactDigest") == before["activeWorker"]["artifactDigest"] for message in new_messages):
    raise SystemExit("no message was acknowledged by the active Worker during the gated candidate attempt")
worker_events = [event for message in new_messages for event in message.get("workerEvents", [])]
if any(event.get("workerArtifactDigest") != before["activeWorker"]["artifactDigest"] for event in worker_events):
    raise SystemExit("a normal Queue message has processing history outside the active Worker")
if any(message.get("workerArtifactDigest") == candidate["artifactDigest"] for message in new_messages):
    raise SystemExit("the gated candidate settled a normal Queue message")
PY

echo "[7/7] verify journal diagnostics and secret redaction"
grep -Fq 'Worker candidate verification failed' "$work_dir/op-07-failed.stderr"
grep -Fq '"outcome": "failed"' "$work_dir/deployment-status.json"
grep -Fq '"recovery": "keep-candidate-gated"' "$work_dir/plan.json"
assert_secret_absent

echo "rejected Worker candidate acceptance passed"
echo "evidence: $work_dir"
