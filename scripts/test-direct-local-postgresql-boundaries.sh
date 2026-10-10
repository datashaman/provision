#!/usr/bin/env bash
# Destructive direct-local proof of two #67 boundaries for the managed PostgreSQL Host tracer:
#   candidate-failure: a candidate that fails mid-transition never disturbs the active generation.
#   forward-only:      a write accepted after cutover exists only in the new generation, so rolling back to
#                      retained or backup material would lose it; the forward-only classification is honest.
# Run on the disposable Host Target as its bootstrap operator.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --artifact-bundle FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME --scenario candidate-failure|forward-only" >&2
  exit 2
}

provision=""
signing_key=""
secret_file=""
artifact_bundle=""
work_dir=""
provision_version=""
confirmed_host=""
scenario=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --artifact-bundle) (($# >= 2)) || usage; artifact_bundle="$2"; shift 2 ;;
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
[[ -n "$artifact_bundle" && -f "$artifact_bundle" && ! -L "$artifact_bundle" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$scenario" == candidate-failure || "$scenario" == forward-only ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local PostgreSQL acceptance requires Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "operator identity is unsupported" >&2; exit 1; }
actual_host="$(hostname)"
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
for path in \
  "$repo_root/examples/host-database-http/application.yaml" \
  "$repo_root/examples/host-database-http/environment.yaml" \
  "$repo_root/examples/host-database-http/revision.yaml" \
  "$repo_root/examples/host-database-http/root.yaml"; do
  [[ -f "$path" && ! -L "$path" ]] || { echo "required acceptance input is missing or unsafe: $path" >&2; exit 1; }
done
command -v curl >/dev/null
command -v python3 >/dev/null
command -v sha256sum >/dev/null
sudo -n true

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
secret_file="$(cd "$(dirname "$secret_file")" && pwd)/$(basename "$secret_file")"
artifact_bundle="$(cd "$(dirname "$artifact_bundle")" && pwd)/$(basename "$artifact_bundle")"
state="$work_dir/state.db"
secret_reference="secret://lab/postgresql-url"
initial_generation=postgresql-17-6-b86568d3e0fe
transition_version=17.7
transition_generation=postgresql-17-7-030da09481c3
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

fail() {
  echo "direct-local PostgreSQL boundary: $*" >&2
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
    if isinstance(value, list):
        try:
            value = value[int(segment)]
        except ValueError as exc:
            raise SystemExit(f"{path}: {field} segment {segment!r} cannot index a list") from exc
    else:
        value = value[segment]
expected = json.loads(expected_json)
if value != expected:
    raise SystemExit(f"{path}: {field} = {value!r}, expected {expected!r}")
PY
}

plan_kind() {
  local path="$1" operation="$2"
  python3 - "$path" "$operation" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    plan = json.load(handle)
for operation in plan["operations"]:
    if operation["id"] == sys.argv[2]:
        print(operation["kind"])
        break
else:
    raise SystemExit(f"operation {sys.argv[2]} is absent")
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
actual = latest.get("outcome", "-")
if actual != outcome:
    raise SystemExit(f"{path}: latest {operation_id} outcome is {actual!r}, expected {outcome!r}")
PY
}

journal_assert_paused() {
  local path="$1" operation_id="$2"
  python3 - "$path" "$operation_id" <<'PY'
import json
import sys

path, operation_id = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    events = json.load(handle)["events"]
last = events[-1]
if last["operationId"] != operation_id or last["kind"] != "intent":
    raise SystemExit(f"{path}: journal must end at {operation_id} intent, got {last['operationId']} {last['kind']}")
if any(e["operationId"] == operation_id and e["kind"] == "outcome" for e in events):
    raise SystemExit(f"{path}: interrupted {operation_id} was marked with an outcome")
if not (last.get("attemptId") and last.get("fencingToken")):
    raise SystemExit(f"{path}: paused {operation_id} lacks attempt/fencing evidence")
PY
}

assert_secret_absent() {
  local password
  password="$(sed -E 's#^[^:]+://[^:]+:([^@]+)@.*#\1#' "$secret_file")"
  [[ -n "$password" ]] || fail "resolved Database secret has no password"
  if grep -R --binary-files=without-match -F "$password" "$work_dir" >/dev/null 2>&1; then
    fail "resolved Database secret appeared in evidence"
  fi
}

write_status() {
  local name="$1" backend="$2" label="$3"
  "$provision" deployment status --plan "$plan_id" --state "$backend" >"$work_dir/$name/$label-status.json"
}

preview_and_approve() {
  local name="$1" backend="$2"
  local directory="$work_dir/$name"
  mkdir -p "$directory"
  chmod 0700 "$directory"
  "$provision" config validate --file "$config_dir/root.yaml" >"$directory/validation.json"
  "$provision" plan preview --file "$config_dir/root.yaml" --state "$backend" >"$directory/plan.json"
  plan_id="$(python3 - "$directory/plan.json" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    print(json.load(handle)["id"])
PY
)"
  [[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity for $name"
  "$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$operator" --state "$backend" --expires-after 2h >"$directory/approval.json"
  json_assert "$directory/approval.json" eligible true
}

prepare_local_database_http_artifact() {
  local artifact_dir="$work_dir/artifact"
  local bundle="$artifact_dir/provision-example-database-http-linux-amd64.tar.gz"
  mkdir -p "$artifact_dir"
  chmod 0700 "$artifact_dir"
  cp "$artifact_bundle" "$bundle"
  chmod 0600 "$bundle"
  local digest
  digest="sha256:$(sha256sum "$bundle" | awk '{print $1}')"
  sudo install -d -o root -g root -m 0755 /var/lib/provision/artifacts/sha256
  sudo install -o root -g root -m 0644 "$bundle" "/var/lib/provision/artifacts/sha256/${digest#sha256:}"
  python3 - "$config_dir/revision.yaml" "$digest" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
digest = sys.argv[2]
lines = []
for line in path.read_text(encoding="utf-8").splitlines():
    stripped = line.strip()
    if stripped.startswith("source:"):
        lines.append("    source: https://artifacts.invalid/provision-example-database-http-linux-amd64.tar.gz")
    elif stripped.startswith("digest:"):
        lines.append(f"    digest: {digest}")
    else:
        lines.append(line)
path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY
  printf '%s\n' "$digest" >"$work_dir/artifact-digest.txt"
}

execute_success() {
  local name="$1" backend="$2" operation="$3"
  local label="${4:-$operation}"
  "$provision" deployment execute \
    --plan "$plan_id" \
    --operation "$operation" \
    --state "$backend" \
    --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" \
    --lease-duration 2m >"$work_dir/$name/$label.json"
  json_assert "$work_dir/$name/$label.json" outcome '"succeeded"'
}

execute_failure() {
  local name="$1" backend="$2" operation="$3" label="$4" expected="$5"
  local output="$work_dir/$name/$label.txt"
  set +e
  "$provision" deployment execute \
    --plan "$plan_id" \
    --operation "$operation" \
    --state "$backend" \
    --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" \
    --lease-duration 2m >"$output" 2>&1
  local result=$?
  set -e
  [[ $result -ne 0 ]] || fail "$name $operation unexpectedly succeeded"
  assert_contains "$output" "$expected"
}

assert_postgresql_active() {
  local label="$1" expected_id="$2"
  local host_output="$work_dir/$label-host.json"
  "$provision" host bootstrap check --local --environment lab --operator "$operator" >"$host_output"
  json_assert "$host_output" ready true
  json_assert "$host_output" database.deployment.active.id "\"$expected_id\""
  json_assert "$host_output" database.deployment.active.health '"healthy"'
  json_assert "$host_output" database.deployment.active.connectivity true
}

assert_http_database_record() {
  local label="$1"
  local output="$work_dir/$label-http-verify.json"
  curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/verify >"$output"
  assert_contains "$output" "provision-lab-data/provision-example-database-http-v1/record-0001"
}

# reset_http_workload removes the initial HTTP generation so the transition Plan can
# redeploy the workload against the new Database; the planner derives the same
# candidate port for an identical artifact, which the active unit would still hold.
# It touches no Database state.
reset_http_workload() {
  local active_unit
  active_unit="$(python3 - <<'PY'
import json
from pathlib import Path

path = Path("/var/lib/provision/environments/lab/active-generation.json")
if path.exists():
    print(json.loads(path.read_text(encoding="utf-8"))["active"]["systemdUnit"])
PY
)"
  if [[ -n "$active_unit" ]]; then
    [[ "$active_unit" =~ ^provision-lab-web-[a-f0-9]{12}\.service$ ]] || fail "active HTTP unit has an unsupported name: $active_unit"
    sudo systemctl stop "$active_unit"
    sudo rm -f "/etc/systemd/system/$active_unit"
    sudo systemctl daemon-reload
  fi
  sudo rm -f /var/lib/provision/environments/lab/active-generation.json
}

cp "$repo_root/examples/host-database-http/application.yaml" "$config_dir/application.yaml"
cp "$repo_root/examples/host-database-http/revision.yaml" "$config_dir/revision.yaml"
cp "$repo_root/examples/host-database-http/root.yaml" "$config_dir/root.yaml"
python3 - "$repo_root/examples/host-database-http/environment.yaml" "$config_dir/environment.yaml" "$operator" <<'PY'
from pathlib import Path
import sys

source = Path(sys.argv[1])
destination = Path(sys.argv[2])
operator = sys.argv[3]
out = []
for line in source.read_text(encoding="utf-8").splitlines():
    stripped = line.strip()
    if stripped.startswith("address:"):
        out.append("    local: true")
    elif stripped.startswith("user:"):
        out.append(f"    user: {operator}")
    else:
        out.append(line)
destination.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
prepare_local_database_http_artifact

"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/initial-host.json"
json_assert "$work_dir/initial-host.json" ready true
json_assert "$work_dir/initial-host.json" database.deployment '{}'
if curl --silent --max-time 1 http://127.0.0.1:18080/verify >/dev/null 2>&1; then
  fail "stable HTTP Endpoint already exists; reset and bootstrap the disposable host before running the matrix"
fi
if find /var/lib/provision/environments/lab/services/postgresql/generations -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null | grep -q .; then
  fail "PostgreSQL generation storage is not empty; reset the disposable host before running the matrix"
fi

# env_run runs a command as the Environment account, as the restricted executor does.
env_run() (
  local uid
  uid="$(id -u provision-lab)"
  cd / && sudo runuser -u provision-lab -- env HOME=/var/lib/provision/runtime/lab "XDG_RUNTIME_DIR=/run/user/$uid" "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus" "$@"
)

env_podman() { env_run podman "$@"; }

sql() {
  env_podman exec "$1" psql -U app -d app -Atc "$2"
}

assert_row() {
  local container="$1" id="$2" expected="$3" count
  count="$(sql "$container" "SELECT count(*) FROM provision_boundary_markers WHERE id = '$id'")"
  [[ "$count" == "$expected" ]] || fail "$container has $count rows for marker $id, expected $expected"
}

start_initial_generation() {
  echo "[1/4] initial generation and an acknowledged pre-transition write"
  preview_and_approve initial "$state"
  for operation in op-01 op-02 op-03 op-04 op-05 op-06 op-07 op-08 op-09; do
    execute_success initial "$state" "$operation"
  done
  assert_postgresql_active initial-final "$initial_generation"
  assert_http_database_record initial-final
  sql provision-lab-postgresql "CREATE TABLE provision_boundary_markers (id text PRIMARY KEY)" >/dev/null
  sql provision-lab-postgresql "INSERT INTO provision_boundary_markers VALUES ('pre-transition')" >/dev/null
  assert_row provision-lab-postgresql pre-transition 1

  echo "[2/4] retarget the Database at PostgreSQL $transition_version and approve the transition"
  reset_http_workload
  python3 - "$config_dir/environment.yaml" "$transition_version" <<'PY'
from pathlib import Path
import sys

path, version = Path(sys.argv[1]), sys.argv[2]
out = []
for line in path.read_text(encoding="utf-8").splitlines():
    out.append(line)
    if line.strip() == "credential: secret://lab/postgresql-url":
        out.append(line[: len(line) - len(line.lstrip())] + f'version: "{version}"')
path.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
  preview_and_approve transition "$state"
}

authority_switch_evidence="/var/lib/provision/environments/lab/services/postgresql/generations/$transition_generation/transition/authority-switch.json"

if [[ "$scenario" == candidate-failure ]]; then
  start_initial_generation
  echo "[3/4] candidate is prepared, then fails before synchronization"
  execute_success transition "$state" op-01
  env_run systemctl --user stop provision-lab-postgresql-candidate.service
  set +e
  "$provision" deployment execute --plan "$plan_id" --operation op-02 --state "$state" --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/transition/op-02-failed.txt" 2>&1
  sync_status=$?
  set -e
  [[ $sync_status -ne 0 ]] || fail "synchronization succeeded although the candidate was stopped"
  write_status transition "$state" after-candidate-failure
  python3 - "$work_dir/transition/after-candidate-failure-status.json" <<'PY'
import json
import sys

events = [e for e in json.load(open(sys.argv[1], encoding="utf-8"))["events"] if e["operationId"] == "op-02"]
outcomes = [e.get("outcome") for e in events if e["kind"] == "outcome"]
if not outcomes or outcomes[-1] not in ("failed", "uncertain"):
    raise SystemExit(f"op-02 must end failed or uncertain, got {outcomes!r}")
PY

  echo "[4/4] the active generation is untouched and the switch cannot proceed"
  assert_postgresql_active candidate-failed "$initial_generation"
  assert_contains "$work_dir/candidate-failed-host.json" "$initial_generation"
  assert_row provision-lab-postgresql pre-transition 1
  remote_switch=0
  sudo test -e "$authority_switch_evidence" && remote_switch=1
  [[ $remote_switch -eq 0 ]] || fail "authority-switch evidence exists after a failed candidate"
  set +e
  "$provision" deployment execute --plan "$plan_id" --operation op-06 --state "$state" --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/transition/op-06-refused.txt" 2>&1
  switch_status=$?
  set -e
  [[ $switch_status -ne 0 ]] || fail "authority switch ran although the candidate never synchronized"
  assert_postgresql_active candidate-failed-after-switch-attempt "$initial_generation"
  sudo test ! -e "$authority_switch_evidence" || fail "authority-switch evidence exists after a refused switch"
  assert_secret_absent
  echo "candidate-failure boundary proved"
else
  start_initial_generation
  echo "[3/4] complete the forward-only Store Transition"
  for operation in op-01 op-02 op-03 op-04 op-05 op-06 op-07 op-08 op-09 op-10 op-11 op-12 op-13 op-14; do
    execute_success transition "$state" "$operation"
  done
  assert_postgresql_active transition-final "$transition_generation"
  json_assert "$work_dir/transition-final-host.json" database.deployment.retained.0.id "\"$initial_generation\""
  json_assert "$work_dir/transition/plan.json" database.storeRollbackGuarantee '"forward-only"'
  json_assert "$work_dir/transition/plan.json" database.forwardCutoverGuarantee '"lossless-after-bounded-write-fence"'
  assert_contains "$work_dir/transition/plan.json" rollback-classification-exposed-separately-from-retention
  assert_row provision-lab-postgresql pre-transition 1

  echo "[4/4] a post-cutover write exists only in the new generation"
  sql provision-lab-postgresql "INSERT INTO provision_boundary_markers VALUES ('post-cutover')" >/dev/null
  assert_row provision-lab-postgresql post-cutover 1
  backup_path="$(python3 - "$work_dir/transition/op-04.json" <<'PY'
import json
import sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["observation"]["backup"]["path"])
PY
)"
  image_reference="$(python3 - "$work_dir/transition/plan.json" <<'PY'
import json
import sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["database"]["imageReference"])
PY
)"
  sudo cat "$backup_path" | env_podman run --rm -i --network none --entrypoint pg_restore "$image_reference" -f - >"$work_dir/pre-cutover-backup.sql"
  assert_contains "$work_dir/pre-cutover-backup.sql" pre-transition
  if grep -Fq post-cutover "$work_dir/pre-cutover-backup.sql"; then
    fail "pre-cutover backup contains the post-cutover write"
  fi
  assert_secret_absent
  echo "forward-only boundary proved: the retained generation and the only backup lack the acknowledged post-cutover write"
fi
echo "evidence: $work_dir"
