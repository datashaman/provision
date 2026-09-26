#!/usr/bin/env bash
# Black-box acceptance for message-level Worker verification and rollback.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --state-seed FILE --work-dir DIRECTORY --target HOST --target-user USER --scenario healthy|rollback" >&2
  exit 2
}

provision="" signing_key="" config="" secret_file="" state_seed="" work_dir="" target="" target_user="" scenario=""
while (($#)); do
  case "$1" in
    --provision) provision="$2"; shift 2 ;;
    --signing-key) signing_key="$2"; shift 2 ;;
    --config) config="$2"; shift 2 ;;
    --secret-file) secret_file="$2"; shift 2 ;;
    --state-seed) state_seed="$2"; shift 2 ;;
    --work-dir) work_dir="$2"; shift 2 ;;
    --target) target="$2"; shift 2 ;;
    --target-user) target_user="$2"; shift 2 ;;
    --scenario) scenario="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -x "$provision" && -f "$signing_key" && -f "$config" && -f "$secret_file" && -f "$state_seed" ]] || usage
[[ "$work_dir" == /* && ! -e "$work_dir" ]] || usage
[[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage
[[ "$scenario" == healthy || "$scenario" == rollback ]] || usage

mkdir -m 0700 "$work_dir"
state="$work_dir/state.db"
cp -- "$state_seed" "$state"
chmod 0600 "$state"
secret_reference="secret://lab/rabbitmq-url"

plan_id() { python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' < "$1"; }
inspect_host() {
  "$provision" host bootstrap check --address "$target" --user "$target_user" --environment lab --operator "$target_user"
}
execute_operation() {
  local operation="$1"
  local args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || args+=(--secret-file "$secret_reference=$secret_file")
  "$provision" "${args[@]}" > "$work_dir/$operation.json"
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

echo "[1/8] inspect the active asynchronous baseline"
inspect_host > "$work_dir/bootstrap-before.json"
"$provision" config validate --file "$config" > "$work_dir/configuration.json"
python3 - "$work_dir/bootstrap-before.json" <<'PY'
import json, sys
deployment = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
worker = deployment.get("activeWorker")
if not worker or worker.get("gate") != "open" or not worker.get("unitActive") or not worker.get("queueConnected"):
    raise SystemExit(f"baseline has no exact open active Worker: {worker!r}")
if deployment.get("candidateWorker"):
    raise SystemExit("baseline already has a Worker candidate")
if not deployment.get("queue", {}).get("ready"):
    raise SystemExit("baseline Queue is not ready")
PY

echo "[2/8] preview and approve the Worker replacement Plan"
"$provision" plan preview --file "$config" --state "$state" > "$work_dir/plan.json"
plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$plan" --actor "$(id -un)" --state "$state" > "$work_dir/approval.json"
python3 - "$work_dir/plan.json" <<'PY'
import json, sys
plan = json.load(open(sys.argv[1], encoding="utf-8"))
expected = ["prepareQueue", "stageArtifact", "installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate", "fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake", "verifyWorkerActive", "retainWorkerPrevious"]
kinds = [operation["kind"] for operation in plan["operations"]]
if kinds != expected:
    raise SystemExit(f"unexpected Worker verification operations: {kinds!r}")
verification = next(operation for operation in plan["operations"] if operation["kind"] == "verifyWorkerActive")
handoff = verification.get("input", {}).get("async", {}).get("workerHandoff", {})
if verification.get("recovery") != "restore-previous-worker-intake" or not handoff.get("queueGenerationId") or not handoff.get("drainOperationDigest") or not handoff.get("worker", {}).get("previous"):
    raise SystemExit("active Worker verification is not bound to exact rollback inputs")
PY

echo "[3/8] install, verify, fence, drain, and activate the candidate"
for operation in op-01 op-02 op-05 op-06 op-07 op-08 op-09 op-10; do
  execute_operation "$operation"
done

candidate_unit="$(python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); print(next(o for o in p["operations"] if o["kind"]=="verifyWorkerActive")["input"]["async"]["workerHandoff"]["worker"]["systemdUnit"])' "$work_dir/plan.json")"
if [[ "$scenario" == rollback ]]; then
  echo "[4/8] inject a post-activation candidate failure"
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" "sudo -n systemctl stop '$candidate_unit'"
else
  echo "[4/8] leave the activated candidate healthy"
fi

echo "[5/8] verify real Queue processing and apply the supported outcome"
if [[ "$scenario" == healthy ]]; then
  execute_operation op-11
  execute_operation op-15
else
  set +e
  execute_operation op-11 2> "$work_dir/op-11.stderr"
  status=$?
  set -e
  [[ $status -ne 0 ]] || { echo "failed candidate verification unexpectedly succeeded" >&2; exit 1; }
fi

echo "[6/8] inspect active, previous, candidate, and unchanged Queue identities"
inspect_host > "$work_dir/bootstrap-after.json"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-after.json" "$work_dir/op-11.json" "$scenario" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
after = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
result = json.load(open(sys.argv[3], encoding="utf-8"))
scenario = sys.argv[4]
observed = result["observation"]
for key in ("id", "generationId", "imageManifest", "serviceUnit", "container", "dataPath"):
    if after["queue"].get(key) != before["queue"].get(key):
        raise SystemExit(f"Queue identity changed at {key}")
if not observed.get("messageId") or not observed.get("publisherConfirmed") or not observed.get("operationDigest") or not observed.get("planId"):
    raise SystemExit(f"Worker verification omitted stable message or operation identity: {observed!r}")
if scenario == "healthy":
    if result.get("outcome") != "succeeded" or observed.get("status") != "healthy" or not observed.get("candidateProcessed") or not observed.get("candidateAcknowledged") or observed.get("rollbackAttempted"):
        raise SystemExit(f"healthy candidate was not proved with real Queue work: {observed!r}")
    if after["activeWorker"].get("id") != observed["candidate"].get("id") or after["previousWorker"].get("id") != observed["previous"].get("id"):
        raise SystemExit("healthy verification did not commit exact active and previous Worker identities")
else:
    if result.get("outcome") != "failed" or observed.get("status") != "rolled-back" or not observed.get("rollbackAttempted") or not observed.get("rollbackSucceeded") or not observed.get("previousProcessed") or not observed.get("previousAcknowledged"):
        raise SystemExit(f"failed candidate was not rolled back and directly verified: {observed!r}")
    if observed.get("restored", {}).get("id") != before["activeWorker"].get("id") or after["activeWorker"].get("id") != before["activeWorker"].get("id"):
        raise SystemExit("rollback did not restore the exact previous Worker identity")
    candidate = after.get("candidateWorker")
    if not candidate or candidate.get("id") != observed["candidate"].get("id") or candidate.get("gate") != "closed" or candidate.get("unitActive"):
        raise SystemExit(f"failed candidate is not durably fenced: {candidate!r}")
PY

echo "[7/8] verify journal outcome and recovery evidence"
"$provision" deployment status --plan "$plan" --state "$state" > "$work_dir/deployment-status.json"
python3 - "$work_dir/deployment-status.json" "$scenario" <<'PY'
import json, sys
status = json.load(open(sys.argv[1], encoding="utf-8"))
scenario = sys.argv[2]
events = [event for event in status["events"] if event.get("operationId") == "op-11" and event.get("kind") == "outcome"]
if len(events) != 1 or events[0].get("outcome") != ("succeeded" if scenario == "healthy" else "failed"):
    raise SystemExit(f"Worker verification journal outcome is wrong: {events!r}")
observation = events[0].get("observation", {})
if observation.get("status") != ("healthy" if scenario == "healthy" else "rolled-back"):
    raise SystemExit(f"Worker verification journal evidence is ambiguous: {observation!r}")
if status.get("rejectedResults"):
    raise SystemExit(f"Worker verification produced rejected results: {status['rejectedResults']!r}")
PY

echo "[8/8] verify secret redaction"
assert_secret_absent

echo "Worker active verification acceptance passed ($scenario)"
echo "evidence: $work_dir"
