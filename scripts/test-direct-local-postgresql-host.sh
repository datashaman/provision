#!/usr/bin/env bash
# Destructive direct-local acceptance matrix for the managed PostgreSQL Host
# tracer. This script must run on the disposable Host Target as its bootstrap
# operator; every Provision operation uses the local Host Handler and the
# restricted executor on the same machine.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --artifact-bundle FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME [--fault-mode before|after]" >&2
  exit 2
}

provision=""
signing_key=""
secret_file=""
artifact_bundle=""
work_dir=""
provision_version=""
confirmed_host=""
fault_mode="after"
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --artifact-bundle) (($# >= 2)) || usage; artifact_bundle="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --fault-mode) (($# >= 2)) || usage; fault_mode="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$provision" && -x "$provision" && -f "$provision" && ! -L "$provision" ]] || usage
[[ -n "$signing_key" && -f "$signing_key" && ! -L "$signing_key" ]] || usage
[[ -n "$secret_file" && -f "$secret_file" && ! -L "$secret_file" ]] || usage
[[ -n "$artifact_bundle" && -f "$artifact_bundle" && ! -L "$artifact_bundle" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$fault_mode" == before || "$fault_mode" == after ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local PostgreSQL acceptance requires Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "operator identity is unsupported" >&2; exit 1; }
actual_host="$(hostname)"
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fault_bin="$repo_root/scripts/direct-local-fault-bin"
for path in \
  "$repo_root/examples/host-database-http/application.yaml" \
  "$repo_root/examples/host-database-http/environment.yaml" \
  "$repo_root/examples/host-database-http/revision.yaml" \
  "$repo_root/examples/host-database-http/root.yaml" \
  "$fault_bin/sudo"; do
  [[ -f "$path" && ! -L "$path" ]] || { echo "required acceptance input is missing or unsafe: $path" >&2; exit 1; }
done
[[ -x "$fault_bin/sudo" ]] || { echo "fault-injection sudo wrapper is not executable" >&2; exit 1; }
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
stale_state="$work_dir/stale-state.db"
secret_reference="secret://lab/postgresql-url"
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

fail() {
  echo "direct-local PostgreSQL matrix: $*" >&2
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

plan_has_operation() {
  local path="$1" operation="$2"
  python3 - "$path" "$operation" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    plan = json.load(handle)
for operation in plan["operations"]:
    if operation["id"] == sys.argv[2]:
        raise SystemExit(0)
raise SystemExit(1)
PY
}

journal_assert_resume() {
  local path="$1" operation_id="$2" outcome="$3"
  python3 - "$path" "$operation_id" "$outcome" <<'PY'
import json
import sys

path, operation_id, outcome = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    status = json.load(handle)
events = [event for event in status["events"] if event["operationId"] == operation_id]
resumed = [event for event in events if event["kind"] == "intent" and event.get("observation", {}).get("resumeOfAttemptId")]
if not resumed:
    raise SystemExit(f"{path}: {operation_id} has no resume provenance")
latest = events[-1]
if latest["kind"] != "outcome" or latest.get("outcome") != outcome:
    raise SystemExit(f"{path}: resumed {operation_id} latest outcome = {latest!r}, expected {outcome}")
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

execute_with_interruption_and_resume() {
  local name="$1" backend="$2" operation="$3"
  local kind marker replay_marker result lease_duration resume_delay
  kind="$(plan_kind "$work_dir/$name/plan.json" "$operation")"
  marker="$work_dir/$name/$operation-$fault_mode-marker"
  lease_duration=5s
  resume_delay=6
  if [[ "$fault_mode" == after ]]; then
    lease_duration=30s
    resume_delay=31
  fi
  set +e
  PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE="$fault_mode" PROVISION_FAULT_MARKER="$marker" \
    "$provision" deployment execute \
      --plan "$plan_id" \
      --operation "$operation" \
      --state "$backend" \
      --signing-key "$signing_key" \
      --secret-file "$secret_reference=$secret_file" \
      --lease-duration "$lease_duration" >"$work_dir/$name/$operation-$fault_mode.txt" 2>&1
  result=$?
  set -e
  [[ $result -ne 0 && -f "$marker" ]] || fail "$operation did not inject $fault_mode interruption"
  write_status "$name" "$backend" "$operation-$fault_mode"
  journal_assert_latest "$work_dir/$name/$operation-$fault_mode-status.json" "$operation" intent -
  journal_assert_paused "$work_dir/$name/$operation-$fault_mode-status.json" "$operation"
  sleep "$resume_delay"
  if [[ "$fault_mode" == after ]]; then
    replay_marker="$work_dir/$name/$operation-unexpected-replay"
    PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$replay_marker" \
      "$provision" deployment resume \
        --plan "$plan_id" \
        --state "$backend" \
        --signing-key "$signing_key" \
        --secret-file "$secret_reference=$secret_file" \
        --lease-duration 2m >"$work_dir/$name/$operation-resumed.json"
    [[ ! -e "$replay_marker" ]] || fail "$operation resume replayed a completed $kind host mutation"
  else
    "$provision" deployment resume \
      --plan "$plan_id" \
      --state "$backend" \
      --signing-key "$signing_key" \
      --secret-file "$secret_reference=$secret_file" \
      --lease-duration 2m >"$work_dir/$name/$operation-resumed.json"
  fi
  json_assert "$work_dir/$name/$operation-resumed.json" outcome '"succeeded"'
  write_status "$name" "$backend" "$operation-resumed"
  journal_assert_resume "$work_dir/$name/$operation-resumed-status.json" "$operation" succeeded
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

assert_backup_restore_evidence() {
  local name="$1" backup="$2" restore="$3"
  json_assert "$work_dir/$name/$backup.json" observation.transitionPhase '"backupDatabase"'
  json_assert "$work_dir/$name/$backup.json" observation.backup.schemaVersion '"provision.dev/database-backup/v1alpha1"'
  json_assert "$work_dir/$name/$backup.json" observation.backup.outsideGeneration true
  json_assert "$work_dir/$name/$restore.json" observation.transitionPhase '"verifyDatabaseRestore"'
  json_assert "$work_dir/$name/$restore.json" observation.restore.schemaVersion '"provision.dev/database-restore-verification/v1alpha1"'
  json_assert "$work_dir/$name/$restore.json" observation.restore.isolated true
  json_assert "$work_dir/$name/$restore.json" observation.restore.verified true
}

assert_transition_result() {
  local name="$1"
  json_assert "$work_dir/$name/op-03-resumed.json" observation.writeFence '"active-database-read-only"'
  json_assert "$work_dir/$name/op-06-resumed.json" observation.transitionPhase '"switchDatabaseAuthority"'
  json_assert "$work_dir/$name/op-07-resumed.json" observation.transitionPhase '"verifyDatabaseActive"'
  json_assert "$work_dir/$name/op-08-resumed.json" observation.transitionPhase '"retainDatabasePrevious"'
  json_assert "$work_dir/$name/op-08-resumed.json" observation.previous.id '"postgresql-17-6-aaaaaaaaaaaa"'
  assert_postgresql_active "$name-final" postgresql-17-6-b86568d3e0fe
  json_assert "$work_dir/$name-final-host.json" database.deployment.retained.0.id '"postgresql-17-6-aaaaaaaaaaaa"'
  assert_http_database_record "$name-final"
}

seed_compatible_active_generation() {
  local evidence="$work_dir/compatible-active-seed.json"
  local uid
  uid="$(id -u provision-lab)"
  sudo -u provision-lab env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR="/run/user/$uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" systemctl --user stop provision-lab-postgresql.service
  sudo python3 - "$evidence" <<'PY'
import json
import shutil
import subprocess
from pathlib import Path

service_root = Path("/var/lib/provision/environments/lab/services/postgresql")
qualified = "postgresql-17-6-b86568d3e0fe"
previous = "postgresql-17-6-aaaaaaaaaaaa"
qualified_root = service_root / "generations" / qualified
previous_root = service_root / "generations" / previous
record_path = qualified_root / "generation.json"
if not record_path.exists():
    raise SystemExit("qualified generation record is absent")
record = json.loads(record_path.read_text(encoding="utf-8"))
previous_root.mkdir(parents=True, exist_ok=True)
old_data = str(previous_root / "data")
new_data = record["dataPath"]
quadlet = Path(record["quadletPath"])
quadlet_text = quadlet.read_text(encoding="utf-8")
if new_data not in quadlet_text:
    raise SystemExit("qualified data path is absent from PostgreSQL Quadlet")
if Path(old_data).exists():
    shutil.rmtree(old_data)
subprocess.run(["cp", "-a", new_data, old_data], check=True)
shutil.rmtree(new_data)
quadlet.write_text(quadlet_text.replace(new_data, old_data), encoding="utf-8")
record["generationId"] = previous
record["dataPath"] = old_data
record_path.unlink()
(previous_root / "generation.json").write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
(previous_root / "generation.json").chmod(0o444)
qualified_root.mkdir(parents=True, exist_ok=True)
summary = {
    "schemaVersion": "provision.dev/direct-local-postgresql-compatible-active-seed/v1alpha1",
    "reason": "issue-75 acceptance needs a controlled compatible active generation so the qualified generation can be prepared as a candidate",
    "previousGeneration": previous,
    "candidateGeneration": qualified,
    "stableServiceUnit": record["serviceUnit"],
    "stableContainer": record["container"],
    "previousDataPath": old_data,
}
Path("/tmp/provision-postgresql-compatible-active-seed.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
  sudo -u provision-lab env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR="/run/user/$uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" systemctl --user daemon-reload
  sudo -u provision-lab env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR="/run/user/$uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" systemctl --user start provision-lab-postgresql.service
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
  cp /tmp/provision-postgresql-compatible-active-seed.json "$evidence"
  assert_postgresql_active compatible-seed postgresql-17-6-aaaaaaaaaaaa
}

create_unsafe_candidate_record() {
  local uid
  uid="$(id -u provision-lab)"
  sudo python3 - "$uid" <<'PY'
import json
import sys
from pathlib import Path

uid = sys.argv[1]
candidate = Path("/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-18-0-bbbbbbbbbbbb")
candidate.mkdir(parents=True, exist_ok=True)
record = {
    "schemaVersion": "provision.dev/host-postgresql-packaging-generation/v1alpha1",
    "logicalId": "provision-lab-data",
    "generationId": "postgresql-18-0-bbbbbbbbbbbb",
    "postgresqlVersion": "18.0",
    "imageIndex": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "imageManifest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "imageReference": "docker.io/library/postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "serviceUnit": "provision-lab-postgresql-candidate.service",
    "container": "provision-lab-postgresql-candidate",
    "account": "provision-lab",
    "dataPath": str(candidate / "data"),
    "quadletPath": f"/etc/containers/systemd/users/{uid}/provision-lab-postgresql-candidate.container",
    "database": "app",
    "credentialReference": "secret://lab/postgresql-url",
    "listenAddress": "127.0.0.1",
    "port": 25433,
    "connectivity": False,
    "durableRestart": False,
    "verified": False,
}
(candidate / "generation.json").write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
(candidate / "generation.json").chmod(0o444)
PY
}

remove_unsafe_candidate_record() {
  sudo rm -rf /var/lib/provision/environments/lab/services/postgresql/generations/postgresql-18-0-bbbbbbbbbbbb
}

write_summary() {
  local destination="$work_dir/support-observation.json"
  python3 - "$destination" "$provision" "$provision_version" "$actual_host" "$(uname -sr)" "$fault_mode" "$work_dir/initial-host.json" "$work_dir/transition-final-host.json" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

destination, provision, version, hostname, kernel, fault_mode, initial_path, final_path = sys.argv[1:]
initial = json.loads(Path(initial_path).read_text(encoding="utf-8"))
final = json.loads(Path(final_path).read_text(encoding="utf-8"))
binary_digest = "sha256:" + hashlib.sha256(Path(provision).read_bytes()).hexdigest()
database = final["database"]
capability = database["capabilities"]
summary = {
    "schemaVersion": "provision.dev/direct-local-postgresql-support-observation/v1alpha1",
    "result": "passed",
    "faultMode": fault_mode,
    "matrix": [
        "clean bootstrap readiness",
        "initial Database generation provisioning",
        "Database-bound HTTP workload binding",
        "unsafe candidate rejection",
        "forward-only Store Transition",
        "backup outside active generation",
        "isolated restore verification",
        "operation interruption and lost-response recovery",
        "stale executor rejection",
        "secret redaction",
    ],
    "provisionSourceVersion": version,
    "provisionBinarySha256": binary_digest,
    "host": hostname,
    "kernel": kernel,
    "os": initial["os"],
    "osVersion": initial["osVersion"],
    "architecture": initial["architecture"],
    "systemdVersion": initial["systemdVersion"],
    "executorDigest": final["executorDigest"],
    "postgresql": {
        "version": capability["postgresqlVersion"],
        "imageIndex": capability["postgresqlImageIndex"],
        "imageManifest": capability["postgresqlImageManifest"],
        "serviceUnit": capability["postgresqlServiceUnit"],
        "container": capability["postgresqlContainer"],
        "topology": {"databaseNodes": 1, "hostFailureTolerance": 0},
        "activeGeneration": database["deployment"]["active"]["id"],
        "retainedGenerations": [item["id"] for item in database["deployment"].get("retained", [])],
    },
    "supportBoundaries": [
        "single Host Target",
        "same PostgreSQL version transition only",
        "forward-only rollback classification",
        "same-host file backup only",
        "no host-loss recovery claim",
        "transition starts from an out-of-band seeded compatible active generation (see compatible-active-seed.json); only one PostgreSQL image is qualified, so the CLI cannot provision a distinct previous generation",
        "ambiguous-state pause is proven by interruption after data-bearing side effects, not by an undecidable observation",
    ],
}
Path(destination).write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
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

echo "[1/6] initial Database generation and workload binding"
preview_and_approve initial "$state"
for operation in op-01 op-02 op-03 op-04 op-05 op-06 op-07 op-08 op-09; do
  execute_success initial "$state" "$operation"
done
assert_backup_restore_evidence initial op-02 op-03
assert_postgresql_active initial-final postgresql-17-6-b86568d3e0fe
assert_http_database_record initial-final

echo "[2/6] controlled compatible-active setup for Store Transition"
seed_compatible_active_generation

echo "[3/6] unsafe candidate rejection"
mkdir -m 0700 "$work_dir/unsafe"
create_unsafe_candidate_record
set +e
"$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$work_dir/unsafe/plan.txt" 2>&1
unsafe_status=$?
set -e
[[ $unsafe_status -ne 0 ]] || fail "unsafe Database candidate was accepted"
assert_contains "$work_dir/unsafe/plan.txt" "failed PostgreSQL compatibility gate"
assert_contains "$work_dir/unsafe/plan.txt" '"candidate"'
remove_unsafe_candidate_record

echo "[4/6] preview transition and stale lineage"
preview_and_approve transition "$state"
transition_plan_id="$plan_id"
preview_and_approve stale "$stale_state"
stale_plan_id="$plan_id"
plan_id="$transition_plan_id"

echo "[5/6] Store Transition with $fault_mode interruption recovery"
for operation in op-01 op-02 op-03 op-04 op-05 op-06 op-07 op-08; do
  execute_with_interruption_and_resume transition "$state" "$operation"
done
for operation in op-09 op-10 op-11 op-12 op-13 op-14; do
  if plan_has_operation "$work_dir/transition/plan.json" "$operation"; then
    execute_success transition "$state" "$operation"
  fi
done
assert_backup_restore_evidence transition op-04-resumed op-05-resumed
assert_transition_result transition

echo "[6/6] stale executor attempt and evidence redaction"
plan_id="$stale_plan_id"
mkdir -p "$work_dir/stale"
chmod 0700 "$work_dir/stale"
execute_failure stale "$stale_state" op-01 op-01-stale 'fencing token is stale'
assert_contains "$work_dir/stale/op-01-stale.txt" 'outcome recorded as uncertain'
write_status stale "$stale_state" final
journal_assert_latest "$work_dir/stale/final-status.json" op-01 outcome uncertain
assert_secret_absent
write_summary

echo "direct-local PostgreSQL $fault_mode matrix passed"
echo "evidence: $work_dir"
