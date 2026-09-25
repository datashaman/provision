#!/usr/bin/env bash
# Destructive SSH acceptance matrix for an already bootstrapped disposable
# Linux Host Target. Provision and its State Backend remain on the controller.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --work-dir EMPTY_PATH --provision-version VERSION --target ADDRESS --target-user USER --confirm-disposable-host HOSTNAME" >&2
  exit 2
}

provision=""
signing_key=""
work_dir=""
provision_version=""
target_address=""
target_user=""
confirmed_host=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target_address="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$provision" && -x "$provision" && -f "$provision" && ! -L "$provision" ]] || usage
[[ -n "$signing_key" && -f "$signing_key" && ! -L "$signing_key" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" ]] || usage
[[ "$target_address" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || usage
[[ "$target_user" =~ ^[a-z_][a-z0-9_-]*$ && -n "$confirmed_host" ]] || usage
[[ -t 0 ]] || { echo "remote acceptance requires a terminal for the deliberate Caddy fault" >&2; exit 1; }

actor="$(id -un)"
[[ "$actor" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "controller identity is unsupported" >&2; exit 1; }
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fixtures="$repo_root/testdata/direct-local-matrix"
fault_bin="$repo_root/scripts/remote-ssh-fault-bin"
for path in "$fixtures/application.yaml" "$fixtures/application-pre-switch-failure.yaml" "$fixtures/environment-remote.yaml.tmpl" "$fixtures/revision-v1.yaml" "$fixtures/revision-v2.yaml" "$fixtures/revision-fail-stable.yaml" "$fixtures/revision-v3.yaml" "$fixtures/root.yaml" "$fault_bin/ssh" "$fault_bin/stop-kill-executor.sh"; do
  [[ -f "$path" && ! -L "$path" ]] || { echo "required acceptance input is missing or unsafe: $path" >&2; exit 1; }
done
[[ -x "$fault_bin/ssh" && -x "$fault_bin/stop-kill-executor.sh" ]] || { echo "SSH fault helpers are not executable" >&2; exit 1; }
for command in curl pgrep python3 ssh ssh-keygen sed sort; do command -v "$command" >/dev/null; done
real_ssh="$(command -v ssh)"

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
state="$work_dir/state.db"
ssh_options=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ConnectTimeout=5 -- "$target_user@$target_address")

remote() {
  "$real_ssh" "${ssh_options[@]}" "$1"
}

fail() {
  echo "remote SSH matrix: $*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f1
  else
    shasum -a 256 "$1" | cut -d ' ' -f1
  fi
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
  local path="$1" operation_id="$2" expected_outcome="$3"
  python3 - "$path" "$operation_id" "$expected_outcome" <<'PY'
import json
import sys

path, operation_id, expected_outcome = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    status = json.load(handle)
events = [event for event in status["events"] if event["operationId"] == operation_id]
resumed = [event for event in events if event["kind"] == "intent" and event.get("observation", {}).get("resumeOfAttemptId")]
if not resumed:
    raise SystemExit(f"{path}: {operation_id} has no resume provenance")
latest = events[-1]
if latest["kind"] != "outcome" or latest.get("outcome") != expected_outcome:
    raise SystemExit(f"{path}: resumed {operation_id} outcome is not {expected_outcome!r}")
PY
}

assert_retention_result() {
  local path="$1" expected_active_revision="$2" expected_previous_revision="$3"
  python3 - "$path" "$expected_active_revision" "$expected_previous_revision" <<'PY'
import calendar
from datetime import datetime
import json
import re
import sys

path, expected_active_revision, expected_previous_revision = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    result = json.load(handle)
observation = result["observation"]
expected = {
    "outcome": "succeeded",
    "status": "retained",
    "policy": "rollback-window",
    "rollbackWindow": "30m0s",
    "stableRouteVerified": True,
    "previousUnitActive": False,
    "previousUnitRetained": True,
    "previousGenerationDirectoryRetained": True,
    "previousManifestRetained": True,
    "previousArtifactRetained": True,
    "restartable": True,
    "cleanupPerformed": False,
}
actual = {"outcome": result["outcome"]}
actual.update({key: observation[key] for key in expected if key != "outcome"})
if actual != expected:
    raise SystemExit(f"{path}: retention evidence differs: {actual!r}")
if observation["active"]["revision"] != expected_active_revision:
    raise SystemExit(f"{path}: retained active revision differs")
if observation["previous"]["revision"] != expected_previous_revision:
    raise SystemExit(f"{path}: retained previous revision differs")
def rfc3339_nanoseconds(value):
    match = re.fullmatch(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(?:Z|\+00:00)", value)
    if not match:
        raise SystemExit(f"{path}: invalid RFC3339 timestamp {value!r}")
    seconds = calendar.timegm(datetime.strptime(match.group(1), "%Y-%m-%dT%H:%M:%S").timetuple())
    fraction = int((match.group(2) or "").ljust(9, "0"))
    return seconds * 1_000_000_000 + fraction

switched_at = rfc3339_nanoseconds(observation["switchedAt"])
retain_until = rfc3339_nanoseconds(observation["retainUntil"])
if retain_until != switched_at + 30 * 60 * 1_000_000_000:
    raise SystemExit(f"{path}: retention deadline is not exactly switch time plus 30 minutes")
if not observation.get("operationDigest", "").startswith("sha256:"):
    raise SystemExit(f"{path}: retention operation digest is absent")
if not observation.get("drainOperationDigest", "").startswith("sha256:"):
    raise SystemExit(f"{path}: drain operation digest is absent")
PY
}

assert_stable_revision() {
  local revision="$1" label="$2"
  local output="$work_dir/$label-stable.json"
  curl --noproxy '*' --fail --silent --show-error --max-time 5 "http://$target_address:18080/verify" >"$output"
  json_assert "$output" revision "\"$revision\""
}

wait_for_caddy_state() {
  local expected="$1"
  for _ in {1..250}; do
    if remote 'systemctl is-active --quiet caddy' >/dev/null 2>&1; then
      [[ "$expected" == active ]] && return 0
    else
      [[ "$expected" == inactive ]] && return 0
    fi
    sleep 0.1
  done
  fail "Caddy did not become $expected"
}

schedule_caddy_outage() {
  printf 'Authenticate once for the deliberate remote Caddy outage. Automatic restoration is scheduled before the stop.\n'
  "$real_ssh" -tt "${ssh_options[@]}" \
    'sudo /usr/bin/systemd-run --quiet --collect --unit=provision-acceptance-caddy-start --on-active=20s /usr/bin/systemctl start caddy && sudo /usr/bin/systemd-run --quiet --collect --unit=provision-acceptance-caddy-stop --on-active=2s /usr/bin/systemctl stop caddy'
  wait_for_caddy_state inactive
}

schedule_executor_stop_and_kill() {
  local fault_script=/tmp/provision-acceptance-executor-fault.sh
  local fault_marker=/tmp/provision-acceptance-executor-stopped
  remote "rm -f $fault_script $fault_marker"
  "$real_ssh" "${ssh_options[@]}" "umask 077; cat >$fault_script; chmod 0700 $fault_script" <"$fault_bin/stop-kill-executor.sh"
  printf 'Authenticate once for the deliberate in-flight executor termination.\n'
  "$real_ssh" -tt "${ssh_options[@]}" \
    "sudo /usr/bin/systemd-run --quiet --collect --unit=provision-acceptance-executor-fault /bin/bash $fault_script $target_user"
}

wait_for_executor_fault_marker() {
  for _ in {1..200}; do
    remote 'test -f /tmp/provision-acceptance-executor-stopped' >/dev/null 2>&1 && return 0
    sleep 0.05
  done
  fail "remote executor was not stopped for the SSH disconnect"
}

wait_for_no_remote_executor() {
  for _ in {1..200}; do
    if "$real_ssh" "${ssh_options[@]}" /bin/bash -s <<'SCRIPT'
for process in /proc/[0-9]*; do
  [[ "$(readlink -f "$process/exe" 2>/dev/null || true)" == /usr/local/libexec/provision-host-executor ]] || continue
  arguments="$(tr '\0' ' ' <"$process/cmdline" 2>/dev/null || true)"
  [[ " $arguments " != *" execute "* ]] || exit 1
done
SCRIPT
    then
      return 0
    fi
    sleep 0.05
  done
  fail "terminated remote executor did not exit"
}

assert_no_gimme() {
  remote 'test ! -e /srv/gimme && test ! -L /srv/gimme && ! systemctl list-unit-files --no-legend "gimme-*" 2>/dev/null | grep -q .' >/dev/null || fail "a Gimme-owned path or unit appeared"
}

assert_artifact_set() {
  local label="$1"
  local expected="$work_dir/$label-expected-artifacts.txt"
  local actual="$work_dir/$label-actual-artifacts.txt"
  case "$label" in
    baseline)
      printf '%s\n' bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5 >"$expected" ;;
    pre-switch|switch-failure)
      printf '%s\n' bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5 f5d67ce429e0eedfd6d2a73be9b775b9a05025853d13c63a004164ce89bc9995 | sort >"$expected" ;;
    drain)
      printf '%s\n' 4e775436b605b9e7ea71c1bdc0941e3f5e345eab05ae264f6cf9fbe56afd6485 bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5 f5d67ce429e0eedfd6d2a73be9b775b9a05025853d13c63a004164ce89bc9995 | sort >"$expected" ;;
    post-switch)
      printf '%s\n' 4e775436b605b9e7ea71c1bdc0941e3f5e345eab05ae264f6cf9fbe56afd6485 b6f188a9b2f582a28618c361aa1b9550a5eb7935bcd39933bae2412487e14ef7 bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5 f5d67ce429e0eedfd6d2a73be9b775b9a05025853d13c63a004164ce89bc9995 | sort >"$expected" ;;
    interruption|stale)
      printf '%s\n' 4e775436b605b9e7ea71c1bdc0941e3f5e345eab05ae264f6cf9fbe56afd6485 b6f188a9b2f582a28618c361aa1b9550a5eb7935bcd39933bae2412487e14ef7 bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5 f5d67ce429e0eedfd6d2a73be9b775b9a05025853d13c63a004164ce89bc9995 | sort >"$expected" ;;
    *) fail "unknown artifact expectation for $label" ;;
  esac
  remote "find /var/lib/provision/artifacts/sha256 -mindepth 1 -maxdepth 1 -type f -printf '%f\\n' | sort" >"$actual"
  cmp -s "$expected" "$actual" || fail "$label has an unexpected remote Artifact set"
  while IFS= read -r digest; do
    [[ "$(remote "sha256sum /var/lib/provision/artifacts/sha256/$digest" | cut -d ' ' -f1)" == "$digest" ]] || fail "$label has a corrupt remote Artifact $digest"
  done <"$expected"
}

assert_caddy_state() {
  local label="$1" host_output="$2"
  local current="$work_dir/$label-caddy-config.json"
  remote 'curl --fail --silent --show-error --max-time 5 http://127.0.0.1:2019/config/' >"$current"
  python3 - "$work_dir/initial-caddy-config.json" "$current" "$host_output" <<'PY'
import json
import sys

initial_path, current_path, host_path = sys.argv[1:]
with open(initial_path, encoding="utf-8") as handle:
    initial = json.load(handle)
with open(current_path, encoding="utf-8") as handle:
    current = json.load(handle)
with open(host_path, encoding="utf-8") as handle:
    host = json.load(handle)
servers = current.get("apps", {}).get("http", {}).get("servers", {})
owned = servers.get("provision-lab-web")
expected = {
    "listen": [":18080"],
    "routes": [{"@id": "provision-lab-web", "handle": [{
        "handler": "reverse_proxy",
        "upstreams": [{"dial": f"127.0.0.1:{host['deployment']['active']['port']}"}],
    }]}],
}
if owned != expected:
    raise SystemExit(f"{current_path}: Plan-owned Caddy server differs from the active Generation")
del servers["provision-lab-web"]
if current != initial:
    raise SystemExit(f"{current_path}: Caddy configuration outside the Plan-owned server changed")
PY
}

assert_retained_files_immutable() {
  local label="$1"
  local current="$work_dir/$label-provision-files.sha256"
  local previous="$work_dir/previous-provision-files.sha256"
  remote 'set -eu; { find /etc/systemd/system -maxdepth 1 -type f -name "provision-lab-web-*.service" -exec sha256sum {} +; find /etc/systemd/system/multi-user.target.wants -maxdepth 1 -type l -name "provision-lab-web-*.service" -printf "symlink %p -> %l\n"; find /var/lib/provision/artifacts/sha256 -mindepth 1 -maxdepth 1 -type f -exec sha256sum {} +; find /var/lib/provision/environments/lab/releases -type f -exec sha256sum {} +; find /var/lib/provision/environments/lab/releases -type d -printf "directory %m %u %g %p\n"; } | sort' >"$current"
  if [[ -f "$previous" ]]; then
    while IFS= read -r retained; do
      grep -Fqx -- "$retained" "$current" || fail "$label changed or removed retained remote material: $retained"
    done <"$previous"
  fi
  cp "$current" "$previous"
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
  "$provision" host bootstrap check --address "$target_address" --user "$target_user" --environment lab --operator "$target_user" >"$host_output"
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
  python3 - "$host_output" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)["sshHostKeyFingerprint"]
if not value.startswith("SHA256:"):
    raise SystemExit(f"{sys.argv[1]}: remote SSH host identity is missing")
PY

  printf '%s\n' "$@" | sed 's/.*-\([0-9a-f]\{12\}\)$/provision-lab-web-\1.service/' | sort >"$expected_units"
  remote "find /etc/systemd/system -maxdepth 1 -type f -name 'provision-lab-web-*.service' -printf '%f\\n' | sort" >"$actual_units"
  cmp -s "$expected_units" "$actual_units" || fail "$label has an unexpected remote systemd unit set"
  printf '%s\n' "$@" | sort >"$expected_releases"
  remote "find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\\n' | sort" >"$actual_releases"
  cmp -s "$expected_releases" "$actual_releases" || fail "$label has an unexpected remote release set"

  assert_artifact_set "$label"
  assert_caddy_state "$label" "$host_output"
  assert_retained_files_immutable "$label"
  assert_no_gimme
  remote 'systemctl is-active --quiet caddy' >/dev/null || fail "$label left remote Caddy inactive"
}

materialize() {
  local name="$1" application="$2" revision="$3"
  local directory="$work_dir/$name"
  mkdir -m 0700 "$directory"
  cp "$fixtures/$application" "$directory/application.yaml"
  cp "$fixtures/$revision" "$directory/revision.yaml"
  cp "$fixtures/root.yaml" "$directory/root.yaml"
  sed -e "s/@OPERATOR@/$target_user/g" -e "s/@ADDRESS@/$target_address/g" "$fixtures/environment-remote.yaml.tmpl" >"$directory/environment.yaml"
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
  "$provision" plan approve --file "$directory/root.yaml" --plan "$plan_id" --actor "$actor" --state "$backend" --expires-after 2h >"$directory/approval.json"
  json_assert "$directory/approval.json" eligible true
}

execute_success() {
  local name="$1" backend="$2" operation="$3" label="${4:-$3}"
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

actual_host="$(remote hostname)"
[[ "$actual_host" == "$confirmed_host" ]] || fail "disposable-host confirmation does not match $actual_host"
assert_no_gimme
"$provision" host bootstrap check --address "$target_address" --user "$target_user" --environment lab --operator "$target_user" >"$work_dir/initial-host.json"
json_assert "$work_dir/initial-host.json" ready true
json_assert "$work_dir/initial-host.json" deployment '{}'
known_fingerprint="$(ssh-keygen -F "$target_address" | ssh-keygen -lf - -E sha256 | awk '$NF == "(ED25519)" {print $2}' | sort -u)"
[[ "$known_fingerprint" =~ ^SHA256:[A-Za-z0-9+/]{43}$ ]] || fail "exactly one pinned ED25519 identity is required for $target_address"
json_assert "$work_dir/initial-host.json" sshHostKeyFingerprint "\"$known_fingerprint\""
remote 'curl --fail --silent --show-error --max-time 5 http://127.0.0.1:2019/config/' >"$work_dir/initial-caddy-config.json"
if curl --noproxy '*' --silent --max-time 1 "http://$target_address:18080/verify" >/dev/null 2>&1; then fail "remote stable Endpoint already exists"; fi
if remote 'find /etc/systemd/system -maxdepth 1 -name "provision-lab-web-*.service" -print -quit | grep -q .' ; then fail "remote Provision application units already exist"; fi
if remote 'find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -print -quit | grep -q .' ; then fail "remote Provision release storage is not empty"; fi

for scenario in baseline pre-switch switch-failure drain post-switch interruption stale; do
  case "$scenario" in
    baseline) materialize "$scenario" application.yaml revision-v1.yaml ;;
    pre-switch) materialize "$scenario" application-pre-switch-failure.yaml revision-v2.yaml ;;
    switch-failure) materialize "$scenario" application.yaml revision-v2.yaml ;;
    drain) materialize "$scenario" application.yaml revision-v3.yaml ;;
    post-switch) materialize "$scenario" application.yaml revision-fail-stable.yaml ;;
    interruption) materialize "$scenario" application.yaml revision-v1.yaml ;;
    stale) materialize "$scenario" application.yaml revision-v1.yaml ;;
  esac
done

echo "[1/7] healthy remote rollout after SSH disconnect and executor restart"
preview_and_approve baseline "$state"
baseline_plan_id="$plan_id"
stale_state="$work_dir/stale/state.db"
preview_and_approve stale "$stale_state"
stale_plan_id="$plan_id"
plan_id="$baseline_plan_id"
schedule_executor_stop_and_kill
set +e
"$provision" deployment execute --plan "$plan_id" --operation op-01 --state "$state" --signing-key "$signing_key" >"$work_dir/baseline/op-01-disconnected.txt" 2>&1 &
provision_pid=$!
set -e
wait_for_executor_fault_marker
ssh_pid="$(pgrep -P "$provision_pid" -x ssh | head -n 1 || true)"
[[ -n "$ssh_pid" ]] || fail "could not identify the in-flight SSH client"
kill -KILL "$ssh_pid"
set +e
wait "$provision_pid"
disconnected_result=$?
set -e
[[ $disconnected_result -ne 0 ]] || fail "in-flight SSH disconnect unexpectedly succeeded"
assert_contains "$work_dir/baseline/op-01-disconnected.txt" 'outcome recorded as uncertain'
write_status baseline "$state" disconnected
journal_assert_latest "$work_dir/baseline/disconnected-status.json" op-01 outcome uncertain
if curl --noproxy '*' --silent --max-time 1 "http://$target_address:18080/verify" >/dev/null 2>&1; then fail "SSH disconnect changed the stable Endpoint"; fi
wait_for_no_remote_executor
"$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" >"$work_dir/baseline/op-01-resumed.json"
json_assert "$work_dir/baseline/op-01-resumed.json" outcome '"succeeded"'
write_status baseline "$state" resumed
journal_assert_resume "$work_dir/baseline/resumed-status.json" op-01 succeeded
remote 'rm -f /tmp/provision-acceptance-executor-fault.sh /tmp/provision-acceptance-executor-stopped'
for operation in op-02 op-03 op-04 op-05 op-06; do execute_success baseline "$state" "$operation"; done
write_status baseline "$state" final
journal_assert_latest "$work_dir/baseline/final-status.json" op-06 outcome succeeded
assert_host_state baseline provision-example-http-v1 - provision-example-http-v1-bac304a88517

echo "[2/7] remote pre-switch verification failure"
preview_and_approve pre-switch "$state"
for operation in op-01 op-02 op-03; do execute_success pre-switch "$state" "$operation"; done
execute_failure pre-switch "$state" op-04 op-04-failed 'host preparation operation failed'
assert_contains "$work_dir/pre-switch/op-04-failed.txt" '"candidateCleaned": true'
write_status pre-switch "$state" final
journal_assert_latest "$work_dir/pre-switch/final-status.json" op-04 outcome failed
assert_host_state pre-switch provision-example-http-v1 - provision-example-http-v1-bac304a88517

echo "[3/7] remote Endpoint switch failure and explicit recovery"
preview_and_approve switch-failure "$state"
for operation in op-01 op-02 op-03 op-04; do execute_success switch-failure "$state" "$operation"; done
schedule_caddy_outage
execute_failure switch-failure "$state" op-05 op-05-caddy-unavailable 'outcome is uncertain'
assert_contains "$work_dir/switch-failure/op-05-caddy-unavailable.txt" '"recoveryAction"'
wait_for_caddy_state active
assert_stable_revision provision-example-http-v1 switch-failure-before-resume
"$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" >"$work_dir/switch-failure/op-05-resumed.json"
json_assert "$work_dir/switch-failure/op-05-resumed.json" outcome '"succeeded"'
execute_success switch-failure "$state" op-06
execute_success switch-failure "$state" op-07
execute_success switch-failure "$state" op-08
assert_retention_result "$work_dir/switch-failure/op-08.json" provision-example-http-v2 provision-example-http-v1
write_status switch-failure "$state" final
journal_assert_resume "$work_dir/switch-failure/final-status.json" op-05 succeeded
journal_assert_latest "$work_dir/switch-failure/final-status.json" op-08 outcome succeeded
assert_host_state switch-failure provision-example-http-v2 provision-example-http-v1 provision-example-http-v1-bac304a88517 provision-example-http-v2-f5d67ce429e0
json_assert "$work_dir/switch-failure-host.json" deployment.previous.unitActive false
if remote 'systemctl is-active --quiet provision-lab-web-bac304a88517.service'; then
  fail "bounded drain left the exact previous remote v1 unit active"
fi

echo "[4/7] remote bounded ordinary-HTTP drain and rollback-window retention"
preview_and_approve drain "$state"
for operation in op-01 op-02 op-03 op-04; do execute_success drain "$state" "$operation"; done
curl --noproxy '*' --fail --silent --show-error --max-time 5 "http://$target_address:18080/slow?seconds=1" >"$work_dir/drain/in-flight.json" &
in_flight_pid=$!
sleep 0.2
execute_success drain "$state" op-05
execute_success drain "$state" op-06
drain_fault_marker="$work_dir/drain/remote-drain-response-lost"
set +e
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$drain_fault_marker" "$provision" deployment execute --plan "$plan_id" --operation op-07 --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/drain/op-07-interrupted.txt" 2>&1
drain_interrupted_result=$?
set -e
[[ $drain_interrupted_result -ne 0 && -f "$drain_fault_marker" ]] || fail "SSH response was not lost after the remote drain"
wait "$in_flight_pid" || fail "ordinary in-flight HTTP request did not complete during the remote bounded handoff"
json_assert "$work_dir/drain/in-flight.json" revision '"provision-example-http-v2"'
write_status drain "$state" interrupted
journal_assert_latest "$work_dir/drain/interrupted-status.json" op-07 intent -
sleep 6
drain_replay_marker="$work_dir/drain/unexpected-drain-replay"
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$drain_replay_marker" "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/drain/op-07-resumed.json"
[[ ! -e "$drain_replay_marker" ]] || fail "resume replayed an already-completed remote drain"
json_assert "$work_dir/drain/op-07-resumed.json" outcome '"succeeded"'
json_assert "$work_dir/drain/op-07-resumed.json" observation.status '"drained"'
json_assert "$work_dir/drain/op-07-resumed.json" observation.mode '"bounded-http"'
retention_fault_marker="$work_dir/drain/remote-retention-response-lost"
set +e
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$retention_fault_marker" "$provision" deployment execute --plan "$plan_id" --operation op-08 --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/drain/op-08-interrupted.txt" 2>&1
retention_interrupted_result=$?
set -e
[[ $retention_interrupted_result -ne 0 && -f "$retention_fault_marker" ]] || fail "SSH response was not lost after remote rollback-window recording"
write_status drain "$state" retention-interrupted
journal_assert_latest "$work_dir/drain/retention-interrupted-status.json" op-08 intent -
sleep 6
retention_replay_marker="$work_dir/drain/unexpected-retention-replay"
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$retention_replay_marker" "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/drain/op-08-resumed.json"
[[ ! -e "$retention_replay_marker" ]] || fail "resume replayed remote rollback-window recording"
assert_retention_result "$work_dir/drain/op-08-resumed.json" provision-example-http-v3 provision-example-http-v2
write_status drain "$state" final
journal_assert_resume "$work_dir/drain/final-status.json" op-07 succeeded
journal_assert_resume "$work_dir/drain/final-status.json" op-08 succeeded
assert_host_state drain provision-example-http-v3 provision-example-http-v2 provision-example-http-v1-bac304a88517 provision-example-http-v2-f5d67ce429e0 provision-example-http-v3-4e775436b605
json_assert "$work_dir/drain-host.json" deployment.previous.unitActive false
if remote 'systemctl is-active --quiet provision-lab-web-f5d67ce429e0.service'; then
  fail "bounded drain left the exact previous remote v2 unit active"
fi

echo "[5/7] remote post-switch rollback after lost SSH response"
preview_and_approve post-switch "$state"
for operation in op-01 op-02 op-03 op-04 op-05; do execute_success post-switch "$state" "$operation"; done
fault_marker="$work_dir/post-switch/rollback-response-lost"
set +e
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$fault_marker" "$provision" deployment execute --plan "$plan_id" --operation op-06 --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/post-switch/op-06-interrupted.txt" 2>&1
interrupted_result=$?
set -e
[[ $interrupted_result -ne 0 && -f "$fault_marker" ]] || fail "SSH response was not lost after the remote rollback"
write_status post-switch "$state" interrupted
journal_assert_latest "$work_dir/post-switch/interrupted-status.json" op-06 intent -
sleep 6
set +e
"$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/post-switch/op-06-resumed.txt" 2>&1
resume_result=$?
set -e
[[ $resume_result -ne 0 ]] || fail "resumed failed post-switch verification unexpectedly succeeded"
assert_contains "$work_dir/post-switch/op-06-resumed.txt" 'previous Generation was restored'
assert_contains "$work_dir/post-switch/op-06-resumed.txt" '"rollbackSucceeded": true'
write_status post-switch "$state" final
journal_assert_resume "$work_dir/post-switch/final-status.json" op-06 failed
assert_host_state post-switch provision-example-http-v3 provision-example-http-v0-3-0-fail-stable provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5 provision-example-http-v1-bac304a88517 provision-example-http-v2-f5d67ce429e0 provision-example-http-v3-4e775436b605

echo "[6/7] remote interruption after traffic switch"
preview_and_approve interruption "$state"
for operation in op-01 op-02 op-03 op-04; do execute_success interruption "$state" "$operation"; done
fault_marker="$work_dir/interruption/switch-response-lost"
set +e
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$fault_marker" "$provision" deployment execute --plan "$plan_id" --operation op-05 --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/interruption/op-05-interrupted.txt" 2>&1
interrupted_result=$?
set -e
[[ $interrupted_result -ne 0 && -f "$fault_marker" ]] || fail "SSH response was not lost after the remote switch"
write_status interruption "$state" interrupted
journal_assert_latest "$work_dir/interruption/interrupted-status.json" op-05 intent -
sleep 6
replay_marker="$work_dir/interruption/unexpected-switch-replay"
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$replay_marker" "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" --lease-duration 5s >"$work_dir/interruption/op-05-resumed.json"
[[ ! -e "$replay_marker" ]] || fail "resume replayed an already-completed remote Endpoint switch"
json_assert "$work_dir/interruption/op-05-resumed.json" outcome '"succeeded"'
execute_success interruption "$state" op-06
write_status interruption "$state" final
journal_assert_resume "$work_dir/interruption/final-status.json" op-05 succeeded
assert_host_state interruption provision-example-http-v1 provision-example-http-v3 provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5 provision-example-http-v1-bac304a88517 provision-example-http-v2-f5d67ce429e0 provision-example-http-v3-4e775436b605

echo "[7/7] stale remote executor attempt"
plan_id="$stale_plan_id"
for operation in op-01 op-02 op-03; do execute_success stale "$stale_state" "$operation"; done
execute_failure stale "$stale_state" op-04 op-04-stale 'fencing token is stale'
assert_contains "$work_dir/stale/op-04-stale.txt" 'outcome recorded as uncertain'
write_status stale "$stale_state" final
journal_assert_latest "$work_dir/stale/final-status.json" op-04 outcome uncertain
assert_host_state stale provision-example-http-v1 provision-example-http-v3 provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5 provision-example-http-v1-bac304a88517 provision-example-http-v2-f5d67ce429e0 provision-example-http-v3-4e775436b605

provision_sha256="$(sha256_file "$provision")"
python3 - "$work_dir/initial-host.json" "$provision_version" "$provision_sha256" "$actual_host" "$(uname -sr)" "$(uname -m)" >"$work_dir/support-observation.json" <<'PY'
import json
import sys

host_path, provision_version, provision_sha256, hostname, controller_kernel, controller_architecture = sys.argv[1:]
with open(host_path, encoding="utf-8") as handle:
    host = json.load(handle)
print(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-support-observation/v1alpha1",
    "result": "passed",
    "transport": "ssh",
    "matrix": [
        "in-flight SSH disconnect and executor restart",
        "healthy",
        "pre-switch failure",
        "switch failure",
        "bounded ordinary-HTTP drain",
        "lost drain response and observation-only resume",
        "rollback-window retention",
        "lost retention response and observation-only resume",
        "post-switch rollback after lost response",
        "switch interruption",
        "stale executor",
    ],
    "provisionSourceVersion": provision_version,
    "provisionBinarySha256": f"sha256:{provision_sha256}",
    "controllerKernel": controller_kernel,
    "controllerArchitecture": controller_architecture,
    "host": hostname,
    "os": host["os"],
    "osVersion": host["osVersion"],
    "architecture": host["architecture"],
    "systemdVersion": host["systemdVersion"],
    "sshServerVersion": host["sshServerVersion"],
    "sshHostKeyFingerprint": host["sshHostKeyFingerprint"],
    "caddyVersion": host["caddyVersion"],
    "executorDigest": host["executorDigest"],
}, indent=2))
PY

echo "remote SSH failure matrix passed"
echo "evidence: $work_dir"
