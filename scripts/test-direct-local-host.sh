#!/usr/bin/env bash
# Destructive acceptance matrix for an already bootstrapped disposable Linux
# Host Target. This script must run on the Host Target as its bootstrap operator.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME" >&2
  exit 2
}

provision=""
signing_key=""
work_dir=""
provision_version=""
confirmed_host=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$provision" && -x "$provision" && -f "$provision" && ! -L "$provision" ]] || usage
[[ -n "$signing_key" && -f "$signing_key" && ! -L "$signing_key" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local acceptance requires Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "operator identity is unsupported" >&2; exit 1; }
actual_host="$(hostname)"
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fixtures="$repo_root/testdata/direct-local-matrix"
fault_bin="$repo_root/scripts/direct-local-fault-bin"
for path in "$fixtures/application.yaml" "$fixtures/application-pre-switch-failure.yaml" "$fixtures/environment.yaml.tmpl" "$fixtures/revision-v1.yaml" "$fixtures/revision-v2.yaml" "$fixtures/revision-fail-stable.yaml" "$fixtures/revision-v3.yaml" "$fixtures/root.yaml" "$fault_bin/sudo"; do
  [[ -f "$path" && ! -L "$path" ]] || { echo "required acceptance input is missing or unsafe: $path" >&2; exit 1; }
done
[[ -x "$fault_bin/sudo" ]] || { echo "fault-injection sudo wrapper is not executable" >&2; exit 1; }
command -v curl >/dev/null
command -v python3 >/dev/null
command -v sha256sum >/dev/null

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
state="$work_dir/state.db"
caddy_stopped=0

restore_caddy() {
  if [[ "$caddy_stopped" == 1 ]]; then
    sudo systemctl start caddy >/dev/null 2>&1 || true
  fi
}
trap restore_caddy EXIT

fail() {
  echo "direct-local matrix: $*" >&2
  exit 1
}

assert_contains() {
  local path="$1" expected="$2"
  grep -Fq "$expected" "$path" || fail "$path does not contain: $expected"
}

json_assert() {
  local path="$1" field="$2" expected_json="$3"
  python3 - "$path" "$field" "$expected_json" <<'PY'
import json
import sys

path, field, expected_json = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    value = json.load(handle)
for segment in field.split("."):
    value = value[segment]
expected = json.loads(expected_json)
if value != expected:
    raise SystemExit(f"{path}: {field} = {value!r}, expected {expected!r}")
PY
}

journal_assert_latest() {
  local path="$1" operation_id="$2" kind="$3" outcome="$4"
  python3 - "$path" "$operation_id" "$kind" "$outcome" <<'PY'
import json
import sys

path, operation_id, kind, outcome = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    status = json.load(handle)
matches = [event for event in status["events"] if event["operationId"] == operation_id]
if not matches:
    raise SystemExit(f"{path}: no event for {operation_id}")
latest = matches[-1]
if latest["kind"] != kind:
    raise SystemExit(f"{path}: latest {operation_id} kind is {latest['kind']!r}, expected {kind!r}")
actual_outcome = latest.get("outcome", "-")
if actual_outcome != outcome:
    raise SystemExit(f"{path}: latest {operation_id} outcome is {actual_outcome!r}, expected {outcome!r}")
PY
}

journal_assert_resume() {
  local path="$1" operation_id="$2"
  python3 - "$path" "$operation_id" <<'PY'
import json
import sys

path, operation_id = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    status = json.load(handle)
events = [event for event in status["events"] if event["operationId"] == operation_id]
resumed = [event for event in events if event["kind"] == "intent" and event.get("observation", {}).get("resumeOfAttemptId")]
if not resumed:
    raise SystemExit(f"{path}: {operation_id} has no resume provenance")
latest = events[-1]
if latest["kind"] != "outcome" or latest.get("outcome") != "succeeded":
    raise SystemExit(f"{path}: resumed {operation_id} did not finish successfully")
PY
}

assert_stable_revision() {
  local revision="$1" label="$2"
  local output="$work_dir/$label-stable.json"
  curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/verify >"$output"
  json_assert "$output" revision "\"$revision\""
}

wait_for_caddy() {
  for _ in {1..50}; do
    if systemctl is-active --quiet caddy && curl --fail --silent --max-time 1 http://127.0.0.1:2019/config/ >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  fail "Caddy did not become ready after fault recovery"
}

assert_no_gimme() {
  [[ ! -e /srv/gimme && ! -L /srv/gimme ]] || fail "Gimme-owned /srv/gimme appeared"
  if systemctl list-unit-files --no-legend 'gimme-*' 2>/dev/null | grep -q .; then
    fail "a Gimme-owned systemd unit appeared"
  fi
}

assert_host_state() {
  local label="$1" active_revision="$2" previous_revision="$3"
  shift 3
  local host_output="$work_dir/$label-host.json"
  local expected_units="$work_dir/$label-expected-units.txt"
  local actual_units="$work_dir/$label-actual-units.txt"
  local expected_releases="$work_dir/$label-expected-releases.txt"
  local actual_releases="$work_dir/$label-actual-releases.txt"

  assert_stable_revision "$active_revision" "$label"
  "$provision" host bootstrap check --local --environment lab --operator "$operator" >"$host_output"
  json_assert "$host_output" ready true
  json_assert "$host_output" deployment.active.revision "\"$active_revision\""
  if [[ "$previous_revision" == - ]]; then
    python3 - "$host_output" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    deployment = json.load(handle)["deployment"]
if "previous" in deployment:
    raise SystemExit(f"{sys.argv[1]}: unexpected previous Generation")
PY
  else
    json_assert "$host_output" deployment.previous.revision "\"$previous_revision\""
  fi

  printf '%s\n' "$@" | sed 's/.*-\([0-9a-f]\{12\}\)$/provision-lab-web-\1.service/' | sort >"$expected_units"
  find /etc/systemd/system -maxdepth 1 -type f -name 'provision-lab-web-*.service' -printf '%f\n' | sort >"$actual_units"
  cmp -s "$expected_units" "$actual_units" || fail "$label has an unexpected Provision systemd unit set"

  printf '%s\n' "$@" | sort >"$expected_releases"
  find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort >"$actual_releases"
  cmp -s "$expected_releases" "$actual_releases" || fail "$label has an unexpected Provision release set"

  assert_no_gimme
  systemctl is-active --quiet caddy || fail "$label left Caddy inactive"
}

materialize() {
  local name="$1" application="$2" revision="$3"
  local directory="$work_dir/$name"
  mkdir -m 0700 "$directory"
  cp "$fixtures/$application" "$directory/application.yaml"
  cp "$fixtures/$revision" "$directory/revision.yaml"
  cp "$fixtures/root.yaml" "$directory/root.yaml"
  sed "s/@OPERATOR@/$operator/g" "$fixtures/environment.yaml.tmpl" >"$directory/environment.yaml"
}

preview_and_approve() {
  local name="$1" backend="$2"
  local directory="$work_dir/$name"
  "$provision" plan preview --file "$directory/root.yaml" --state "$backend" >"$directory/plan.json"
  plan_id="$(python3 - "$directory/plan.json" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    print(json.load(handle)["id"])
PY
)"
  [[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity for $name"
  "$provision" plan approve --file "$directory/root.yaml" --plan "$plan_id" --actor "$operator" --state "$backend" --expires-after 2h >"$directory/approval.json"
  json_assert "$directory/approval.json" eligible true
}

execute_success() {
  local name="$1" backend="$2" operation="$3"
  local label="${4:-$operation}"
  "$provision" deployment execute --plan "$plan_id" --operation "$operation" --state "$backend" --signing-key "$signing_key" >"$work_dir/$name/$label.json"
  json_assert "$work_dir/$name/$label.json" outcome '"succeeded"'
}

execute_failure() {
  local name="$1" backend="$2" operation="$3" label="$4" expected="$5"
  local output="$work_dir/$name/$label.txt"
  set +e
  "$provision" deployment execute --plan "$plan_id" --operation "$operation" --state "$backend" --signing-key "$signing_key" >"$output" 2>&1
  local result=$?
  set -e
  [[ $result -ne 0 ]] || fail "$name $operation unexpectedly succeeded"
  assert_contains "$output" "$expected"
}

write_status() {
  local name="$1" backend="$2" label="$3"
  "$provision" deployment status --plan "$plan_id" --state "$backend" >"$work_dir/$name/$label-status.json"
}

for scenario in baseline pre-switch switch-failure post-switch interruption stale; do
  case "$scenario" in
    baseline) materialize "$scenario" application.yaml revision-v1.yaml ;;
    pre-switch) materialize "$scenario" application-pre-switch-failure.yaml revision-v2.yaml ;;
    switch-failure) materialize "$scenario" application.yaml revision-v2.yaml ;;
    post-switch) materialize "$scenario" application.yaml revision-fail-stable.yaml ;;
    interruption) materialize "$scenario" application.yaml revision-v3.yaml ;;
    stale) materialize "$scenario" application.yaml revision-v1.yaml ;;
  esac
done

sudo -v
assert_no_gimme
"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/initial-host.json"
json_assert "$work_dir/initial-host.json" ready true
json_assert "$work_dir/initial-host.json" deployment '{}'
if curl --silent --max-time 1 http://127.0.0.1:18080/verify >/dev/null 2>&1; then
  fail "stable Endpoint already exists; reset and bootstrap the disposable host before running the matrix"
fi
if find /etc/systemd/system -maxdepth 1 -name 'provision-lab-web-*.service' -print -quit | grep -q .; then
  fail "Provision application units already exist; reset the disposable host before running the matrix"
fi
if find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  fail "Provision release storage is not empty; reset the disposable host before running the matrix"
fi

echo "[1/6] healthy rollout"
preview_and_approve baseline "$state"
baseline_plan_id="$plan_id"
stale_state="$work_dir/stale/state.db"
preview_and_approve stale "$stale_state"
stale_plan_id="$plan_id"
plan_id="$baseline_plan_id"
for operation in op-01 op-02 op-03 op-04 op-05 op-06; do execute_success baseline "$state" "$operation"; done
write_status baseline "$state" final
journal_assert_latest "$work_dir/baseline/final-status.json" op-06 outcome succeeded
assert_host_state baseline provision-example-http-v1 - \
  provision-example-http-v1-bac304a88517

echo "[2/6] pre-switch verification failure"
preview_and_approve pre-switch "$state"
for operation in op-01 op-02 op-03; do execute_success pre-switch "$state" "$operation"; done
execute_failure pre-switch "$state" op-04 op-04-failed 'host preparation operation failed'
assert_contains "$work_dir/pre-switch/op-04-failed.txt" '"candidateCleaned": true'
[[ ! -e /etc/systemd/system/provision-lab-web-f5d67ce429e0.service ]] || fail "failed pre-switch candidate unit was retained"
[[ ! -e /var/lib/provision/environments/lab/releases/provision-example-http-v2-f5d67ce429e0 ]] || fail "failed pre-switch candidate release was retained"
write_status pre-switch "$state" final
journal_assert_latest "$work_dir/pre-switch/final-status.json" op-04 outcome failed
assert_host_state pre-switch provision-example-http-v1 - \
  provision-example-http-v1-bac304a88517

echo "[3/6] Endpoint switch failure and explicit recovery"
preview_and_approve switch-failure "$state"
for operation in op-01 op-02 op-03 op-04; do execute_success switch-failure "$state" "$operation"; done
sudo systemctl stop caddy
caddy_stopped=1
execute_failure switch-failure "$state" op-05 op-05-caddy-unavailable 'outcome is uncertain'
assert_contains "$work_dir/switch-failure/op-05-caddy-unavailable.txt" '"outcome": "uncertain"'
assert_contains "$work_dir/switch-failure/op-05-caddy-unavailable.txt" '"recoveryAction"'
sudo systemctl start caddy
caddy_stopped=0
wait_for_caddy
assert_stable_revision provision-example-http-v1 switch-failure-before-resume
"$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" >"$work_dir/switch-failure/op-05-resumed.json"
json_assert "$work_dir/switch-failure/op-05-resumed.json" outcome '"succeeded"'
execute_success switch-failure "$state" op-06
write_status switch-failure "$state" final
journal_assert_resume "$work_dir/switch-failure/final-status.json" op-05
assert_host_state switch-failure provision-example-http-v2 provision-example-http-v1 \
  provision-example-http-v1-bac304a88517 \
  provision-example-http-v2-f5d67ce429e0

echo "[4/6] post-switch failure and bounded rollback"
preview_and_approve post-switch "$state"
for operation in op-01 op-02 op-03 op-04 op-05; do execute_success post-switch "$state" "$operation"; done
execute_failure post-switch "$state" op-06 op-06-rolled-back 'previous Generation was restored'
assert_contains "$work_dir/post-switch/op-06-rolled-back.txt" '"status": "rolled-back"'
assert_contains "$work_dir/post-switch/op-06-rolled-back.txt" '"rollbackSucceeded": true'
write_status post-switch "$state" final
journal_assert_latest "$work_dir/post-switch/final-status.json" op-06 outcome failed
assert_host_state post-switch provision-example-http-v2 provision-example-http-v0-3-0-fail-stable \
  provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5 \
  provision-example-http-v1-bac304a88517 \
  provision-example-http-v2-f5d67ce429e0

echo "[5/6] process interruption after traffic switch"
preview_and_approve interruption "$state"
for operation in op-01 op-02 op-03 op-04; do execute_success interruption "$state" "$operation"; done
fault_marker="$work_dir/interruption/host-switch-completed"
set +e
PATH="$fault_bin:$PATH" PROVISION_FAULT_MARKER="$fault_marker" "$provision" deployment execute --plan "$plan_id" --operation op-05 --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/interruption/op-05-interrupted.txt" 2>&1
interrupted_result=$?
set -e
[[ $interrupted_result -ne 0 && -f "$fault_marker" ]] || fail "process interruption did not occur after the host switch"
write_status interruption "$state" interrupted
journal_assert_latest "$work_dir/interruption/interrupted-status.json" op-05 intent -
sleep 6
replay_marker="$work_dir/interruption/unexpected-replay"
PATH="$fault_bin:$PATH" PROVISION_FAULT_MARKER="$replay_marker" "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/interruption/op-05-resumed.json"
[[ ! -e "$replay_marker" ]] || fail "resume replayed an already-completed Endpoint switch"
json_assert "$work_dir/interruption/op-05-resumed.json" outcome '"succeeded"'
execute_success interruption "$state" op-06
write_status interruption "$state" final
journal_assert_resume "$work_dir/interruption/final-status.json" op-05
assert_host_state interruption provision-example-http-v3 provision-example-http-v2 \
  provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5 \
  provision-example-http-v1-bac304a88517 \
  provision-example-http-v2-f5d67ce429e0 \
  provision-example-http-v3-4e775436b605

echo "[6/6] stale executor attempt"
plan_id="$stale_plan_id"
for operation in op-01 op-02 op-03 op-04; do execute_success stale "$stale_state" "$operation"; done
execute_failure stale "$stale_state" op-05 op-05-stale 'fencing token is stale'
assert_contains "$work_dir/stale/op-05-stale.txt" 'outcome recorded as uncertain'
write_status stale "$stale_state" final
journal_assert_latest "$work_dir/stale/final-status.json" op-05 outcome uncertain
assert_host_state stale provision-example-http-v3 provision-example-http-v2 \
  provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5 \
  provision-example-http-v1-bac304a88517 \
  provision-example-http-v2-f5d67ce429e0 \
  provision-example-http-v3-4e775436b605

printf '%s\n' \
  "Provision version: $provision_version" \
  "Provision binary SHA-256: $(sha256sum "$provision" | cut -d ' ' -f1)" \
  "Host: $actual_host" \
  "Kernel: $(uname -sr)" \
  "Architecture: $(uname -m)" \
  'Matrix: healthy, pre-switch failure, switch failure, post-switch rollback, process interruption, stale executor' \
  'Result: passed' >"$work_dir/support-observation.txt"

echo "direct-local failure matrix passed"
echo "evidence: $work_dir"
