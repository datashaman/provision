#!/usr/bin/env bash
# Black-box acceptance for observe-before-retry Worker handoff recovery.
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

script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fault_bin="$script_root/remote-ssh-fault-bin"
real_ssh="$(command -v ssh)"
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
  "$provision" "${args[@]}" >"$work_dir/$operation.json"
}
status() {
  "$provision" deployment status --plan "$plan" --state "$state" >"$1"
}
assert_interrupted_intent() {
  python3 - "$1" "$2" <<'PY'
import json, sys
events = [e for e in json.load(open(sys.argv[1], encoding="utf-8"))["events"] if e["operationId"] == sys.argv[2]]
if not events or events[-1]["kind"] != "intent":
    raise SystemExit(f"expected latest {sys.argv[2]} event to be an interrupted intent: {events!r}")
PY
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
interrupt_before_and_after() {
  local operation="$1" expected="$2"
  local before_marker="$work_dir/$operation.before-dispatch" after_marker="$work_dir/$operation.after-host-completion"
  set +e
  PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=before PROVISION_FAULT_MARKER="$before_marker" \
    "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.before.txt" 2>&1
  local before_status=$?
  set -e
  [[ $before_status -ne 0 && -f "$before_marker" ]] || { echo "$operation was not interrupted before Host dispatch" >&2; exit 1; }
  status "$work_dir/$operation.before-status.json"
  assert_interrupted_intent "$work_dir/$operation.before-status.json" "$operation"
  expire_test_lease

  set +e
  PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
    "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.after.txt" 2>&1
  local after_status=$?
  set -e
  [[ $after_status -ne 0 && -f "$after_marker" ]] || { echo "$operation was not interrupted after Host completion" >&2; exit 1; }
  status "$work_dir/$operation.after-status.json"
  assert_interrupted_intent "$work_dir/$operation.after-status.json" "$operation"
  expire_test_lease

  set +e
  "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.resumed.json" 2>"$work_dir/$operation.resumed.stderr"
  local resumed_status=$?
  set -e
  if [[ "$expected" == succeeded ]]; then
    [[ $resumed_status -eq 0 ]] || { cat "$work_dir/$operation.resumed.stderr" >&2; exit 1; }
  else
    [[ $resumed_status -ne 0 ]] || { echo "$operation reconstructed a failed outcome as success" >&2; exit 1; }
  fi
  python3 - "$work_dir/$operation.resumed.json" "$expected" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
if result.get("outcome") != sys.argv[2]:
    raise SystemExit(f"resumed outcome differs: {result!r}")
PY
}

echo "[1/8] inspect exact asynchronous baseline and inventory"
inspect_host >"$work_dir/bootstrap-before.json"
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
  "sudo -n find /etc/systemd/system -maxdepth 1 -type f -name 'provision-lab-consumer-*.service' -printf '%f\\n' | sort; sudo -n find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\\n' | sort; sudo -n find /var/lib/provision/artifacts/sha256 -maxdepth 1 -type f -printf '%f\\n' | sort" >"$work_dir/inventory-before.txt"
"$provision" config validate --file "$config" >"$work_dir/configuration.json"

echo "[2/8] preview and approve exact Worker replacement"
"$provision" plan preview --file "$config" --state "$state" >"$work_dir/plan.json"
plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$plan" --actor "$(id -un)" --state "$state" >"$work_dir/approval.json"
python3 - "$work_dir/plan.json" <<'PY'
import json, sys
kinds = [o["kind"] for o in json.load(open(sys.argv[1], encoding="utf-8"))["operations"]]
expected = ["prepareQueue", "stageArtifact", "installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate", "fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake", "verifyWorkerActive", "retainWorkerPrevious"]
if kinds != expected:
    raise SystemExit(f"unexpected Worker recovery Plan: {kinds!r}")
PY

echo "[3/8] prepare exact candidate"
for operation in op-01 op-02 op-05 op-06 op-07; do execute_operation "$operation"; done

echo "[4/8] recover interruptions before and after fence, drain, and activation"
interrupt_before_and_after op-08 succeeded
interrupt_before_and_after op-09 succeeded
interrupt_before_and_after op-10 succeeded

candidate_unit="$(python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); print(next(o for o in p["operations"] if o["kind"]=="verifyWorkerActive")["input"]["async"]["workerHandoff"]["worker"]["systemdUnit"])' "$work_dir/plan.json")"
if [[ "$scenario" == rollback ]]; then
  echo "[5/8] fail candidate and recover a completed rollback without replay"
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" "sudo -n systemctl stop '$candidate_unit'"
  interrupt_before_and_after op-11 failed
else
  echo "[5/8] recover completed post-activation verification without replay"
  interrupt_before_and_after op-11 succeeded
fi

echo "[6/8] recover rollback-window retention when applicable"
if [[ "$scenario" == healthy ]]; then
  interrupt_before_and_after op-15 succeeded
else
  if "$provision" deployment execute --plan "$plan" --operation op-15 --state "$state" --signing-key "$signing_key" >"$work_dir/op-15-unexpected.json" 2>"$work_dir/op-15-blocked.stderr"; then
    echo "retention became executable after a rolled-back verification" >&2
    exit 1
  fi
fi

echo "[7/8] prove strictly increasing fencing and resume lineage"
status "$work_dir/deployment-status.json"
python3 - "$work_dir/deployment-status.json" "$scenario" <<'PY'
import json, sys
status = json.load(open(sys.argv[1], encoding="utf-8"))
scenario = sys.argv[2]
checked = ["op-08", "op-09", "op-10", "op-11"] + (["op-15"] if scenario == "healthy" else [])
for operation in checked:
    events = [e for e in status["events"] if e["operationId"] == operation]
    intents = [e for e in events if e["kind"] == "intent"]
    outcomes = [e for e in events if e["kind"] == "outcome"]
    tokens = [e["fencingToken"] for e in intents]
    if len(intents) != 3 or len(outcomes) != 1 or tokens != sorted(set(tokens)):
        raise SystemExit(f"{operation} did not resume through three strictly fenced attempts: {events!r}")
    for previous, resumed in zip(intents, intents[1:]):
        if resumed.get("observation", {}).get("resumeOfAttemptId") != previous["attemptId"]:
            raise SystemExit(f"{operation} lost resume lineage: {events!r}")
expected = "failed" if scenario == "rollback" else "succeeded"
op11 = [e for e in status["events"] if e["operationId"] == "op-11" and e["kind"] == "outcome"][-1]
if op11["outcome"] != expected:
    raise SystemExit(f"verification outcome differs: {op11!r}")
PY

echo "[8/8] verify exact Queue, Artifact, unit, and retained-Generation inventory"
inspect_host >"$work_dir/bootstrap-after.json"
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
  "sudo -n find /etc/systemd/system -maxdepth 1 -type f -name 'provision-lab-consumer-*.service' -printf '%f\\n' | sort; sudo -n find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\\n' | sort; sudo -n find /var/lib/provision/artifacts/sha256 -maxdepth 1 -type f -printf '%f\\n' | sort" >"$work_dir/inventory-after.txt"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-after.json" "$work_dir/plan.json" "$work_dir/inventory-before.txt" "$work_dir/inventory-after.txt" "$scenario" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
after = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
plan = json.load(open(sys.argv[3], encoding="utf-8"))
scenario = sys.argv[6]
handoff = next(o for o in plan["operations"] if o["kind"] == "verifyWorkerActive")["input"]["async"]["workerHandoff"]
candidate, previous = handoff["worker"], handoff["worker"]["previous"]
for key in ("id", "generationId", "imageManifest", "serviceUnit", "container", "dataPath"):
    if after["queue"].get(key) != before["queue"].get(key):
        raise SystemExit(f"Queue changed at {key}")
if scenario == "healthy":
    if after["activeWorker"]["id"] != candidate["generationId"] or after["previousWorker"]["id"] != previous["id"] or not after["previousWorker"].get("restartable"):
        raise SystemExit("healthy recovery did not retain exact active and previous Worker generations")
else:
    if after["activeWorker"]["id"] != previous["id"] or after.get("candidateWorker", {}).get("id") != candidate["generationId"]:
        raise SystemExit("rollback recovery did not preserve exact restored and fenced candidate identities")
before_inventory = set(open(sys.argv[4], encoding="utf-8").read().splitlines())
after_inventory = set(open(sys.argv[5], encoding="utf-8").read().splitlines())
expected_additions = {candidate["systemdUnit"], candidate["generationId"], candidate["artifactDigest"].removeprefix("sha256:")}
unexpected = after_inventory - before_inventory - expected_additions
missing = expected_additions - after_inventory
if unexpected or missing:
    raise SystemExit(f"abandoned or missing attempt inventory: unexpected={sorted(unexpected)!r} missing={sorted(missing)!r}")
PY

echo "Worker handoff interruption recovery passed ($scenario)"
echo "evidence: $work_dir"
