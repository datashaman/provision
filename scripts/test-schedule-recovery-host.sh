#!/usr/bin/env bash
# Black-box acceptance for Schedule interruption and restart recovery.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --state-seed FILE --work-dir DIRECTORY (--target HOST --target-user USER [--incus-host HOST --incus-instance NAME] | --local --operator USER)" >&2
  exit 2
}

provision="" signing_key="" config="" secret_file="" state_seed="" work_dir="" target="" target_user="" incus_host="" incus_instance="" operator="" local_target=0
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
    --local) local_target=1; shift ;;
    --operator) operator="$2"; shift 2 ;;
    --incus-host) incus_host="$2"; shift 2 ;;
    --incus-instance) incus_instance="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -x "$provision" && -f "$signing_key" && -f "$config" && -f "$secret_file" && -f "$state_seed" ]] || usage
[[ "$work_dir" == /* && ! -e "$work_dir" ]] || usage
if [[ "$local_target" == 1 ]]; then
  [[ -z "$target" && -z "$target_user" && -z "$incus_host" && -z "$incus_instance" && "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage
else
  [[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ && -z "$operator" ]] || usage
  operator="$target_user"
fi
if [[ -n "$incus_host" || -n "$incus_instance" ]]; then
  [[ "$incus_host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$incus_instance" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || usage
fi

script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ "$local_target" == 1 ]]; then
  fault_bin="$script_root/direct-local-fault-bin"
  [[ -x "$fault_bin/sudo" ]] || { echo "direct-local fault helper is missing" >&2; exit 1; }
else
  fault_bin="$script_root/remote-ssh-fault-bin"
  real_ssh="$(command -v ssh)"
fi
mkdir -m 0700 "$work_dir"
state="$work_dir/state.db"
cp -- "$state_seed" "$state"
chmod 0600 "$state"
secret_reference="secret://lab/rabbitmq-url"

plan_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["id"])' "$1"; }
operation_id() {
  python3 - "$work_dir/plan.json" "$1" <<'PY'
import json, sys
plan = json.load(open(sys.argv[1], encoding="utf-8"))
kind = sys.argv[2]
matches = [operation["id"] for operation in plan["operations"] if operation["kind"] == kind]
if len(matches) != 1:
    raise SystemExit(f"expected one {kind} operation, got {matches!r}")
print(matches[0])
PY
}
inspect_host() {
  if [[ "$local_target" == 1 ]]; then
    "$provision" host bootstrap check --local --environment lab --operator "$operator"
  else
    "$provision" host bootstrap check --address "$target" --user "$target_user" --environment lab --operator "$operator"
  fi
}
execute_operation() {
  local operation="$1"
  local args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != "$prepare_queue" ]] || args+=(--secret-file "$secret_reference=$secret_file")
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
  local operation="$1"
  local before_marker="$work_dir/$operation.before-dispatch"
  local after_marker="$work_dir/$operation.after-host-completion"
  set +e
  if [[ "$local_target" == 1 ]]; then
    PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE=before PROVISION_FAULT_MARKER="$before_marker" \
      "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.before.txt" 2>&1
  else
    PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=before PROVISION_FAULT_MARKER="$before_marker" \
      "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.before.txt" 2>&1
  fi
  local before_status=$?
  set -e
  [[ $before_status -ne 0 && -f "$before_marker" ]] || { echo "$operation was not interrupted before Host dispatch" >&2; exit 1; }
  status "$work_dir/$operation.before-status.json"
  assert_interrupted_intent "$work_dir/$operation.before-status.json" "$operation"
  expire_test_lease

  set +e
  if [[ "$local_target" == 1 ]]; then
    PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
      "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.after.txt" 2>&1
  else
    PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
      "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.after.txt" 2>&1
  fi
  local after_status=$?
  set -e
  [[ $after_status -ne 0 && -f "$after_marker" ]] || { echo "$operation was not interrupted after Host completion" >&2; exit 1; }
  status "$work_dir/$operation.after-status.json"
  assert_interrupted_intent "$work_dir/$operation.after-status.json" "$operation"
  expire_test_lease

  "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.resumed.json"
  python3 - "$work_dir/$operation.resumed.json" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
if result.get("outcome") != "succeeded":
    raise SystemExit(f"resumed operation did not succeed: {result!r}")
PY
}
interrupt_after_completion() {
  local operation="$1"
  local after_marker="$work_dir/$operation.after-host-completion"
  set +e
  if [[ "$local_target" == 1 ]]; then
    PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
      "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.after.txt" 2>&1
  else
    PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$after_marker" \
      "$provision" deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.after.txt" 2>&1
  fi
  local after_status=$?
  set -e
  [[ $after_status -ne 0 && -f "$after_marker" ]] || { echo "$operation was not interrupted after Host completion" >&2; exit 1; }
  status "$work_dir/$operation.after-status.json"
  assert_interrupted_intent "$work_dir/$operation.after-status.json" "$operation"
  expire_test_lease

  "$provision" deployment resume --plan "$plan" --state "$state" --signing-key "$signing_key" --lease-duration 2m >"$work_dir/$operation.resumed.json"
  python3 - "$work_dir/$operation.resumed.json" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
if result.get("outcome") != "succeeded":
    raise SystemExit(f"resumed operation did not succeed: {result!r}")
PY
}
ledger_snapshot() {
  local destination="$1"
  if [[ "$local_target" == 1 ]]; then
    # shellcheck disable=SC2024 # The redirect writes to operator-owned evidence; sudo is only for reading the ledger.
    sudo -n python3 - <<'PY' >"$destination"
import json, sqlite3, sys
ledger = "/var/lib/provision/environments/lab/schedules/every-minute/ledger.db"
connection = sqlite3.connect(ledger)
rows = connection.execute("""
select o.id, o.invocation_id, o.task_generation_id, o.fencing_token, o.disposition, i.outcome
from occurrences o join invocations i on i.id = o.invocation_id
where o.schedule = 'every-minute'
order by o.due_at
""").fetchall()
attempts = {}
for invocation_id, number, outcome, systemd_unit, systemd_invocation_id in connection.execute("""
select a.invocation_id, a.number, a.outcome, a.systemd_unit, coalesce(l.systemd_invocation_id, '')
from attempts a left join attempt_launches l on l.invocation_id = a.invocation_id and l.number = a.number
order by a.invocation_id, a.number
"""):
    attempts.setdefault(invocation_id, []).append({
        "number": number,
        "outcome": outcome,
        "systemdUnit": systemd_unit,
        "systemdInvocationId": systemd_invocation_id,
    })
connection.close()
json.dump({"count": len(rows), "last": rows[-1] if rows else None, "rows": rows, "attempts": attempts}, sys.stdout, indent=2)
print()
PY
  else
    ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
      "sudo -n python3 - '$destination'" <<'PY' >"$destination"
import json, sqlite3, sys
ledger = "/var/lib/provision/environments/lab/schedules/every-minute/ledger.db"
connection = sqlite3.connect(ledger)
rows = connection.execute("""
select o.id, o.invocation_id, o.task_generation_id, o.fencing_token, o.disposition, i.outcome
from occurrences o join invocations i on i.id = o.invocation_id
where o.schedule = 'every-minute'
order by o.due_at
""").fetchall()
attempts = {}
for invocation_id, number, outcome, systemd_unit, systemd_invocation_id in connection.execute("""
select a.invocation_id, a.number, a.outcome, a.systemd_unit, coalesce(l.systemd_invocation_id, '')
from attempts a left join attempt_launches l on l.invocation_id = a.invocation_id and l.number = a.number
order by a.invocation_id, a.number
"""):
    attempts.setdefault(invocation_id, []).append({
        "number": number,
        "outcome": outcome,
        "systemdUnit": systemd_unit,
        "systemdInvocationId": systemd_invocation_id,
    })
connection.close()
json.dump({
    "count": len(rows),
    "last": rows[-1] if rows else None,
    "rows": rows,
    "attempts": attempts,
}, sys.stdout, indent=2)
print()
PY
  fi
}
ledger_count() {
  python3 - "$1" <<'PY'
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["count"])
PY
}
wait_for_ledger_growth() {
  local before="$1" destination="$2"
  for _ in $(seq 1 100); do
    ledger_snapshot "$destination"
    local current
    current="$(ledger_count "$destination")"
    if (( current > before )); then
      return 0
    fi
    sleep 2
  done
  echo "Schedule ledger did not grow while waiting for the stable timer" >&2
  exit 1
}
wait_for_ssh() {
  for _ in $(seq 1 90); do
    if ssh -o BatchMode=yes -o ConnectTimeout=2 -o StrictHostKeyChecking=yes "$target_user@$target" true >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "target SSH did not become ready after reboot" >&2
  exit 1
}
restart_local_schedule_runtime() {
  sudo -n systemctl restart provision-lab-every-minute.timer
}

echo "[1/8] inspect active Schedule baseline"
inspect_host >"$work_dir/bootstrap-before.json"
"$provision" config validate --file "$config" >"$work_dir/configuration.json"
ledger_snapshot "$work_dir/ledger-before.json"

echo "[2/8] preview and approve Task-only Schedule handoff Plan"
"$provision" plan preview --file "$config" --state "$state" >"$work_dir/plan.json"
plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$plan" --actor "$(id -un)" --state "$state" >"$work_dir/approval.json"
python3 - "$work_dir/plan.json" "$work_dir/bootstrap-before.json" <<'PY'
import json, sys
plan = json.load(open(sys.argv[1], encoding="utf-8"))
before = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
kinds = [operation["kind"] for operation in plan["operations"]]
expected = ["prepareQueue", "stageArtifact", "installTaskGeneration", "verifyTaskGeneration", "handoffSchedule", "verifySchedule"]
if kinds != expected:
    raise SystemExit(f"unexpected Schedule recovery Plan: {kinds!r}")
handoff = next(operation for operation in plan["operations"] if operation["kind"] == "handoffSchedule")
schedule = handoff["input"]["async"]["schedule"]
if schedule["previous"]["taskGenerationId"] != before["activeTask"]["id"]:
    raise SystemExit("Schedule handoff is not fenced to the active baseline Task")
if schedule["taskGenerationId"] == before["activeTask"]["id"]:
    raise SystemExit("Task-only Plan did not select a new Task generation")
PY
prepare_queue="$(operation_id prepareQueue)"
stage_task="$(operation_id stageArtifact)"
install_task="$(operation_id installTaskGeneration)"
verify_task="$(operation_id verifyTaskGeneration)"
handoff_schedule="$(operation_id handoffSchedule)"
verify_schedule="$(operation_id verifySchedule)"

echo "[3/8] install and verify the candidate Task generation"
for operation in "$prepare_queue" "$stage_task" "$install_task" "$verify_task"; do execute_operation "$operation"; done

echo "[4/8] recover interrupted Schedule handoff from durable host evidence"
interrupt_before_and_after "$handoff_schedule"

echo "[5/8] recover interrupted Schedule verification from occurrence and Task state"
interrupt_after_completion "$verify_schedule"
ledger_snapshot "$work_dir/ledger-after-verify.json"
after_verify_count="$(ledger_count "$work_dir/ledger-after-verify.json")"

echo "[6/8] prove the pinned timer applet keeps recording while management CLI is absent"
wait_for_ledger_growth "$after_verify_count" "$work_dir/ledger-without-cli.json"
inspect_host >"$work_dir/bootstrap-before-runtime-restart.json"

if [[ "$local_target" == 1 ]]; then
  echo "[7/8] restart the stable Schedule runtime and prove it resumes"
  restart_local_schedule_runtime
else
  echo "[7/8] reboot the Host Target and prove the stable timer resumes"
fi
if [[ "$local_target" == 0 && -n "$incus_host" ]]; then
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$incus_host" "incus restart '$incus_instance' --timeout 60"
elif [[ "$local_target" == 0 ]]; then
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" "sudo -n reboot" || true
fi
[[ "$local_target" == 1 ]] || wait_for_ssh
before_runtime_restart_count="$(ledger_count "$work_dir/ledger-without-cli.json")"
wait_for_ledger_growth "$before_runtime_restart_count" "$work_dir/ledger-after-runtime-restart.json"

echo "[8/8] verify status reconstructs active Schedule state without rewriting history"
inspect_host >"$work_dir/bootstrap-after-runtime-restart.json"
status "$work_dir/deployment-status.json"
ledger_snapshot "$work_dir/ledger-after-status.json"
python3 - "$work_dir/bootstrap-before.json" "$work_dir/bootstrap-before-runtime-restart.json" "$work_dir/bootstrap-after-runtime-restart.json" "$work_dir/ledger-without-cli.json" "$work_dir/ledger-after-runtime-restart.json" "$work_dir/ledger-after-status.json" "$work_dir/deployment-status.json" "$work_dir/plan.json" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))["async"]["deployment"]
before_runtime_restart = json.load(open(sys.argv[2], encoding="utf-8"))["async"]["deployment"]
after = json.load(open(sys.argv[3], encoding="utf-8"))["async"]["deployment"]
without_cli = json.load(open(sys.argv[4], encoding="utf-8"))
after_runtime_restart = json.load(open(sys.argv[5], encoding="utf-8"))
after_status = json.load(open(sys.argv[6], encoding="utf-8"))
status = json.load(open(sys.argv[7], encoding="utf-8"))
plan = json.load(open(sys.argv[8], encoding="utf-8"))
handoff = next(operation for operation in plan["operations"] if operation["kind"] == "handoffSchedule")["input"]["async"]["schedule"]
if after["schedule"]["taskGenerationId"] != handoff["taskGenerationId"]:
    raise SystemExit("post-runtime-restart Schedule does not target the handed-off Task generation")
if after["schedule"]["fencingToken"] <= before["schedule"]["fencingToken"]:
    raise SystemExit("Schedule fencing token did not advance")
for key in ("taskGenerationId", "fencingToken", "timerUnit", "appletDigest", "ledgerSchema", "timezone", "expression", "daylightSaving", "overlap", "retry", "missedRun", "failure"):
    if after["schedule"].get(key) != before_runtime_restart["schedule"].get(key):
        raise SystemExit(f"Schedule {key} changed across runtime restart: before={before_runtime_restart['schedule'].get(key)!r} after={after['schedule'].get(key)!r}")
if after["activeTask"]["id"] != after["schedule"]["taskGenerationId"]:
    raise SystemExit("status did not reconstruct the active Task from the Schedule fence")
if after["activeWorker"]["id"] != before["activeWorker"]["id"] or after["queue"]["id"] != before["queue"]["id"]:
    raise SystemExit("Task/Schedule recovery unexpectedly changed Worker or Queue identity")
if after_runtime_restart["count"] <= without_cli["count"]:
    raise SystemExit("Schedule ledger did not grow across runtime restart")
open_invocations = set()
for before_row, status_row in zip(after_runtime_restart["rows"], after_status["rows"]):
    open_occurrence = before_row[4] in {"recorded", "running"} or before_row[5] in {"pending", "running"}
    if open_occurrence:
        open_invocations.add(before_row[1])
        if status_row[:4] != before_row[:4]:
            raise SystemExit(f"management status rewrote in-flight occurrence identity: before={before_row!r} after={status_row!r}")
        allowed_dispositions = {"recorded", "running", "succeeded"} if before_row[4] == "recorded" else {"running", "succeeded"}
        allowed_outcomes = {"pending", "running", "succeeded"} if before_row[5] == "pending" else {"running", "succeeded"}
        if status_row[4] not in allowed_dispositions or status_row[5] not in allowed_outcomes:
            raise SystemExit(f"management status rewrote in-flight occurrence history: before={before_row!r} after={status_row!r}")
        continue
    if status_row != before_row:
        raise SystemExit(f"management status rewrote completed occurrence history: before={before_row!r} after={status_row!r}")
for invocation_id in set(after_runtime_restart["attempts"]) | set(after_status["attempts"]):
    attempts = after_runtime_restart["attempts"].get(invocation_id)
    status_attempts = after_status["attempts"].get(invocation_id)
    if attempts == status_attempts:
        continue
    if invocation_id in open_invocations and attempts is None:
        if not status_attempts or any(attempt.get("outcome") not in {"running", "succeeded"} for attempt in status_attempts):
            raise SystemExit(f"management status rewrote in-flight attempt history for {invocation_id}: before={attempts!r} after={status_attempts!r}")
        continue
    if invocation_id in open_invocations and all(attempt.get("outcome") == "running" for attempt in attempts):
        if not status_attempts or len(status_attempts) != len(attempts):
            raise SystemExit(f"management status rewrote in-flight attempt history for {invocation_id}: before={attempts!r} after={status_attempts!r}")
        for before_attempt, status_attempt in zip(attempts, status_attempts):
            comparable = dict(status_attempt)
            comparable["outcome"] = before_attempt.get("outcome")
            comparable["systemdInvocationId"] = before_attempt.get("systemdInvocationId", "")
            if comparable != before_attempt or status_attempt.get("outcome") not in {"running", "succeeded"}:
                raise SystemExit(f"management status rewrote in-flight attempt history for {invocation_id}: before={before_attempt!r} after={status_attempt!r}")
        continue
    raise SystemExit(f"management status rewrote completed attempt history for {invocation_id}: before={attempts!r} after={status_attempts!r}")
events = status["events"]
for operation_id in [op["id"] for op in plan["operations"] if op["kind"] in {"handoffSchedule", "verifySchedule"}]:
    kind = next(op["kind"] for op in plan["operations"] if op["id"] == operation_id)
    operation_events = [event for event in events if event["operationId"] == operation_id]
    intents = [event for event in operation_events if event["kind"] == "intent"]
    outcomes = [event for event in operation_events if event["kind"] == "outcome"]
    expected_intents = 3 if kind == "handoffSchedule" else 2
    if len(intents) != expected_intents or len(outcomes) != 1:
        raise SystemExit(f"{operation_id} did not preserve interrupted resume evidence: {operation_events!r}")
PY

echo "Schedule interruption and restart recovery passed"
echo "evidence: $work_dir"
