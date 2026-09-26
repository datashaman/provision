#!/usr/bin/env bash
# Black-box acceptance for a bounded Worker intake handoff.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --state-seed FILE --work-dir DIRECTORY --target HOST --target-user USER --scenario complete|deadline" >&2
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
scenario=""
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
    --scenario) (($# >= 2)) || usage; scenario="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -x "$provision" && -f "$signing_key" && -f "$config" && -f "$secret_file" && -f "$state_seed" ]] || usage
[[ "$work_dir" == /* && ! -e "$work_dir" ]] || usage
[[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage
[[ "$scenario" == complete || "$scenario" == deadline ]] || usage

mkdir -m 0700 "$work_dir"
state="$work_dir/state.db"
cp -- "$state_seed" "$state"
chmod 0600 "$state"
secret_reference="secret://lab/rabbitmq-url"
remote_worker_root="/var/lib/provision/environments/lab/async/worker"
remote_worker_records="/var/lib/provision/environments/lab/workers"
previous_worker_id="provision-example-async-v1-2fcb2cec1d3d"
remote_task_evidence="/var/lib/provision/environments/lab/async/task/evidence.jsonl"
remote_credential="/var/lib/provision/environments/lab/credentials/rabbitmq-url"

plan_id() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' < "$1"
}

inspect_host() {
  "$provision" host bootstrap check \
    --address "$target" --user "$target_user" --environment lab --operator "$target_user"
}

execute_operation() {
  local operation="$1"
  local args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || args+=(--secret-file "$secret_reference=$secret_file")
  "$provision" "${args[@]}" > "$work_dir/$operation.json"
}

message_id() {
  local invocation="$1"
  python3 - "$invocation" <<'PY'
import hashlib, sys
revision = "provision-example-async-v1"
artifact = "sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5"
raw = (f"provision-example-async\0{revision}\0{artifact}\0{sys.argv[1]}\0" + "1").encode()
print("msg-" + hashlib.sha256(raw).hexdigest())
PY
}

publish_message() {
  local invocation="$1"
  local behavior="$2"
  local unit="provision-issue42-${scenario}-${invocation}"
  local task_generation="/var/lib/provision/environments/lab/releases/provision-example-async-v1-ce1dc7e13900"
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
    "sudo -n systemd-run --quiet --wait --collect --unit '$unit' --service-type=oneshot --property=User=provision-lab --property=Group=provision-lab --property=LoadCredentialEncrypted=rabbitmq-url:$remote_credential --property=StandardOutput=append:$remote_task_evidence '$task_generation/provision-example-async-task' --broker-url-file '/run/credentials/$unit.service/rabbitmq-url' --queue provision-lab-messages --application-revision provision-example-async-v1 --artifact-digest sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5 --invocation '$invocation' --count 1 --behavior '$behavior'"
}

capture_worker_evidence() {
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
    "sudo -n cat '$remote_worker_root/evidence.jsonl'" > "$1"
}

wait_for_event() {
  local message="$1"
  local event="$2"
  local digest="$3"
  local destination="$4"
  local deadline=$((SECONDS + 90))
  while ((SECONDS < deadline)); do
    capture_worker_evidence "$destination"
    if python3 - "$destination" "$message" "$event" "$digest" <<'PY'
import json, sys
path, message_id, wanted_event, digest = sys.argv[1:]
for line in open(path, encoding="utf-8"):
    item = json.loads(line)
    if item.get("messageId") == message_id and item.get("event") == wanted_event and item.get("workerArtifactDigest") == digest:
        raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for Worker event=$event message=$message digest=$digest" >&2
  return 1
}

wait_for_drain_record() {
  local message="$1"
  local destination="$2"
  local deadline=$((SECONDS + 180))
  while ((SECONDS < deadline)); do
    if ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
      "sudo -n cat '$remote_worker_records/$previous_worker_id.drain.json'" > "$destination" 2>/dev/null &&
      python3 - "$destination" "$message" <<'PY'
import json, sys
record = json.load(open(sys.argv[1], encoding="utf-8"))
if record.get("inFlightMessageId") != sys.argv[2] or not record.get("completionDeadline") or not record.get("deadline") or record.get("completedAt"):
    raise SystemExit(1)
PY
    then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for durable Worker drain record for message=$message" >&2
  return 1
}

release_held_message() {
  local message="$1"
  local key
  key="$(printf '%s' "$message" | shasum -a 256 | awk '{print $1}')"
  printf '%s\n' "$message" | ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
    "sudo -n install -o provision-lab -g provision-lab -m 0640 /dev/stdin '$remote_worker_root/holds/$key.release'"
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

echo "[1/9] inspect the active asynchronous baseline"
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

echo "[2/9] preview and approve the complete Worker replacement Plan"
"$provision" plan preview --file "$config" --state "$state" > "$work_dir/plan.json"
plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$plan" --actor "$(id -un)" --state "$state" > "$work_dir/approval.json"
python3 - "$work_dir/plan.json" "$target" <<'PY'
import hashlib, json, sys
plan = json.load(open(sys.argv[1], encoding="utf-8"))
expected = ["prepareQueue", "stageArtifact", "installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate", "fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake", "verifyWorkerActive", "retainWorkerPrevious"]
kinds = [operation["kind"] for operation in plan["operations"]]
if kinds != expected:
    raise SystemExit(f"unexpected Worker handoff operations: {kinds!r}")
dependencies = {operation["kind"]: operation.get("dependsOn", []) for operation in plan["operations"]}
wanted = {"fenceWorkerIntake": ["op-07"], "drainWorkerPrevious": ["op-08"], "activateWorkerIntake": ["op-09"], "verifyWorkerActive": ["op-10"], "retainWorkerPrevious": ["op-11"]}
for kind, dependency in wanted.items():
    if dependencies[kind] != dependency:
        raise SystemExit(f"{kind} dependencies are {dependencies[kind]!r}; want {dependency!r}")
handoff = next(operation for operation in plan["operations"] if operation["kind"] == "fenceWorkerIntake")["input"]["async"]["workerHandoff"]
if not handoff.get("queueGenerationId") or not handoff.get("worker", {}).get("previous") or not handoff.get("rollbackWindow"):
    raise SystemExit("Worker handoff does not pin Queue, previous Worker, and rollback window")
drain = next(operation for operation in plan["operations"] if operation["kind"] == "drainWorkerPrevious")
drain_digest = "sha256:" + hashlib.sha256(json.dumps(drain, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
for kind in ("activateWorkerIntake", "verifyWorkerActive", "retainWorkerPrevious"):
    operation = next(operation for operation in plan["operations"] if operation["kind"] == kind)
    bound = operation["input"]["async"].get("workerHandoff")
    if not bound or bound.get("drainOperationDigest") != drain_digest:
        raise SystemExit(f"{kind} is not bound to exact drain operation {drain_digest}")
target = plan.get("target", {})
if target.get("kind") != "host" or target.get("address") != sys.argv[2]:
    raise SystemExit(f"Plan target does not match {sys.argv[2]!r}: {target!r}")
PY

echo "[3/9] install and verify the gated candidate"
for operation in op-01 op-02 op-05 op-06 op-07; do
  execute_operation "$operation"
done

old_digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["async"]["deployment"]["activeWorker"]["artifactDigest"])' "$work_dir/bootstrap-before.json")"
candidate_digest="$(python3 -c 'import json,sys; print(next(o for o in json.load(open(sys.argv[1]))["operations"] if o["kind"] == "startWorkerCandidate")["input"]["async"]["worker"]["artifactDigest"])' "$work_dir/plan.json")"
before_invocation="issue42-${scenario}-before"
held_invocation="issue42-${scenario}-held"
during_invocation="issue42-${scenario}-during"
after_invocation="issue42-${scenario}-after"
before_message="$(message_id "$before_invocation")"
held_message="$(message_id "$held_invocation")"
during_message="$(message_id "$during_invocation")"
after_message="$(message_id "$after_invocation")"

echo "[4/9] account for work before the fence and hold one old-Worker delivery"
publish_message "$before_invocation" process
wait_for_event "$before_message" acknowledged "$old_digest" "$work_dir/worker-before.jsonl"
publish_message "$held_invocation" hold
wait_for_event "$held_message" held "$old_digest" "$work_dir/worker-held.jsonl"

echo "[5/9] fence old intake and prove it claims no work after the fence"
execute_operation op-08
publish_message "$during_invocation" process
sleep 2
capture_worker_evidence "$work_dir/worker-after-fence.jsonl"
python3 - "$work_dir/worker-after-fence.jsonl" "$during_message" "$old_digest" <<'PY'
import json, sys
events = [json.loads(line) for line in open(sys.argv[1], encoding="utf-8")]
if any(event.get("messageId") == sys.argv[2] and event.get("workerArtifactDigest") == sys.argv[3] for event in events):
    raise SystemExit("old Worker claimed a message accepted after its durable intake fence")
PY

echo "[6/9] finish or release the old Worker's bounded in-flight delivery"
if [[ "$scenario" == complete ]]; then
  execute_operation op-09 &
  drain_pid=$!
  wait_for_drain_record "$held_message" "$work_dir/drain-in-progress.json"
  release_held_message "$held_message"
  wait "$drain_pid"
  wait_for_event "$held_message" acknowledged "$old_digest" "$work_dir/worker-drained.jsonl"
else
  execute_operation op-09
  python3 - "$work_dir/op-09.json" "$held_message" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
observation = result["observation"]
if result.get("outcome") != "succeeded" or observation.get("status") != "previous-released" or not observation.get("boundElapsed") or observation.get("releasedMessageId") != sys.argv[2]:
    raise SystemExit(f"deadline drain did not durably release the exact held message: {observation!r}")
PY
  wait_for_event "$held_message" requeued "$old_digest" "$work_dir/worker-released.jsonl"
  release_held_message "$held_message"
fi

echo "[7/9] activate only the candidate and account for work during and after handoff"
execute_operation op-10
execute_operation op-11
wait_for_event "$during_message" acknowledged "$candidate_digest" "$work_dir/worker-during.jsonl"
if [[ "$scenario" == deadline ]]; then
  wait_for_event "$held_message" acknowledged "$candidate_digest" "$work_dir/worker-redelivered.jsonl"
fi
publish_message "$after_invocation" process
wait_for_event "$after_message" acknowledged "$candidate_digest" "$work_dir/worker-after.jsonl"

echo "[8/9] retain the exact previous generation through the rollback window"
execute_operation op-15
inspect_host > "$work_dir/bootstrap-after.json"
capture_worker_evidence "$work_dir/worker-final.jsonl"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-after.json" "$work_dir/op-08.json" "$work_dir/op-09.json" "$work_dir/op-15.json" "$work_dir/worker-final.jsonl" "$before_message" "$held_message" "$during_message" "$after_message" "$old_digest" "$candidate_digest" "$scenario" <<'PY'
import datetime, json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
after = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
fence = json.load(open(sys.argv[3], encoding="utf-8"))["observation"]
drain = json.load(open(sys.argv[4], encoding="utf-8"))["observation"]
retention = json.load(open(sys.argv[5], encoding="utf-8"))["observation"]
events = [json.loads(line) for line in open(sys.argv[6], encoding="utf-8")]
before_id, held_id, during_id, after_id, old_digest, candidate_digest, scenario = sys.argv[7:]
for key in ("id", "generationId", "imageManifest", "serviceUnit", "container", "dataPath"):
    if after["queue"].get(key) != before["queue"].get(key):
        raise SystemExit(f"Queue identity changed at {key}")
if after["activeWorker"]["artifactDigest"] != candidate_digest or after["activeWorker"]["gate"] != "open":
    raise SystemExit("candidate is not the exact open active Worker")
previous = after.get("previousWorker")
if not previous or previous.get("artifactDigest") != old_digest or previous.get("unitActive") or previous.get("gate") != "closed" or not previous.get("restartable") or not previous.get("retainUntil"):
    raise SystemExit(f"previous Worker is not stopped and restartable through the rollback window: {previous!r}")
if retention.get("status") != "previous-retained" or retention.get("queueGenerationId") != before["queue"]["generationId"] or not retention.get("operationDigest") or not retention.get("drainOperationDigest") or retention.get("rollbackWindow") != "30m0s":
    raise SystemExit(f"retention did not bind the unchanged Queue generation: {retention!r}")
if not drain.get("operationDigest") or not drain.get("planId") or not drain.get("drainStartedAt") or not drain.get("completionDeadline") or not drain.get("drainDeadline") or not drain.get("drainCompletedAt"):
    raise SystemExit(f"drain omitted durable operation identity or deadline evidence: {drain!r}")
if drain.get("inFlightMessageId") != held_id:
    raise SystemExit(f"drain did not bind the exact held in-flight message: {drain!r}")
def parse_time(value):
    value = value.removesuffix("Z")
    if "." in value:
        whole, fraction = value.split(".", 1)
        value = whole + "." + (fraction + "000000")[:6]
    return datetime.datetime.fromisoformat(value).replace(tzinfo=datetime.timezone.utc)
started_at = parse_time(drain["drainStartedAt"])
completion_deadline = parse_time(drain["completionDeadline"])
deadline = parse_time(drain["drainDeadline"])
completed_at = parse_time(drain["drainCompletedAt"])
if not started_at < completion_deadline < deadline or not started_at <= completed_at <= deadline:
    raise SystemExit(f"Worker drain or release escaped its declared deadline: {drain!r}")
if fence.get("status") != "previous-fenced" or fence.get("inFlightMessageId") != held_id or fence.get("previous", {}).get("inFlight") != 1:
    raise SystemExit(f"intake fence did not preserve the exact held in-flight delivery: {fence!r}")

def matching(message, event, digest):
    return [item for item in events if item.get("messageId") == message and item.get("event") == event and item.get("workerArtifactDigest") == digest]

if not matching(before_id, "acknowledged", old_digest):
    raise SystemExit("pre-fence message was not acknowledged by the old Worker")
if not matching(held_id, "held", old_digest):
    raise SystemExit("in-flight message was not held by the old Worker")
if any(item.get("messageId") in {during_id, after_id} and item.get("workerArtifactDigest") == old_digest for item in events):
    raise SystemExit("old Worker claimed work accepted during or after the fence")
if not matching(during_id, "acknowledged", candidate_digest) or not matching(after_id, "acknowledged", candidate_digest):
    raise SystemExit("candidate did not exclusively acknowledge during/after messages")
if scenario == "complete":
    if drain.get("status") != "previous-drained" or drain.get("boundElapsed") or drain.get("releasedMessageId") or not matching(held_id, "acknowledged", old_digest):
        raise SystemExit(f"old Worker did not complete the held delivery inside the bound: {drain!r}")
else:
    if drain.get("status") != "previous-released" or not drain.get("boundElapsed") or not drain.get("releaseStartedAt") or drain.get("releasedMessageId") != held_id:
        raise SystemExit(f"deadline did not release the exact stable message identity: {drain!r}")
    release_started_at = parse_time(drain["releaseStartedAt"])
    if not completion_deadline <= release_started_at <= completed_at:
        raise SystemExit(f"bounded release timing is inconsistent: {drain!r}")
    if not matching(held_id, "requeued", old_digest) or not matching(held_id, "acknowledged", candidate_digest):
        raise SystemExit("released delivery was not requeued by the old Worker and acknowledged by the candidate under the same identity")
PY

echo "[9/9] verify journal outcomes and secret redaction"
"$provision" deployment status --plan "$plan" --state "$state" > "$work_dir/deployment-status.json"
python3 - "$work_dir/deployment-status.json" <<'PY'
import json, sys
status = json.load(open(sys.argv[1], encoding="utf-8"))
outcomes = {event["operationId"]: event.get("outcome") for event in status["events"] if event.get("kind") == "outcome"}
for operation in ("op-08", "op-09", "op-10", "op-11", "op-15"):
    if outcomes.get(operation) != "succeeded":
        raise SystemExit(f"{operation} did not finish successfully: {outcomes.get(operation)!r}")
if status.get("rejectedResults"):
    raise SystemExit(f"handoff produced rejected results: {status['rejectedResults']!r}")
PY
assert_secret_absent

echo "Worker intake handoff acceptance passed ($scenario)"
echo "evidence: $work_dir"
