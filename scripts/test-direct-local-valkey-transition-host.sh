#!/usr/bin/env bash
# Destructive direct-local failure matrix for the managed Valkey Store Transition
# (issue #100). Runs on the disposable Host Target as its bootstrap operator. The
# initial generation is provisioned by the CLI (no out-of-band seeding), then
# every transition operation is interrupted per --fault-mode.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME --fault-mode before|after|undecidable" >&2
  exit 2
}

provision="" signing_key="" secret_file="" work_dir="" provision_version="" confirmed_host="" fault_mode=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
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
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$fault_mode" =~ ^(before|after|undecidable)$ ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local Valkey matrix requires Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "operator identity is unsupported" >&2; exit 1; }
actual_host="$(hostname)"
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fault_bin="$repo_root/scripts/direct-local-fault-bin"
[[ -x "$fault_bin/sudo" ]] || { echo "fault-injection sudo wrapper is not executable" >&2; exit 1; }
command -v python3 >/dev/null
sudo -n true

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
abs() { echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }
provision="$(abs "$provision")"; signing_key="$(abs "$signing_key")"; secret_file="$(abs "$secret_file")"
state="$work_dir/state.db"
secret_reference="secret://lab/valkey-url"
initial_version=9.1.1
initial_generation="" transition_generation=""
transition_version=9.1.2
generations_root=/var/lib/provision/environments/lab/services/valkey/generations
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

fail() { echo "direct-local Valkey matrix ($fault_mode): $*" >&2; exit 1; }
# shellcheck source=lib/matrix-helpers.sh
source "$script_dir/lib/matrix-helpers.sh"







preview_and_approve() {
  local name="$1" directory="$work_dir/$1"
  mkdir -m 0700 "$directory"
  "$provision" config validate --file "$config_dir/root.yaml" >"$directory/validation.json"
  "$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$directory/plan.json"
  plan_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$directory/plan.json")"
  [[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity for $name"
  "$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$operator" --state "$state" --expires-after 2h >"$directory/approval.json"
  json_assert "$directory/approval.json" eligible true
}

write_status() { "$provision" deployment status --plan "$plan_id" --state "$state" >"$work_dir/$1/$2-status.json"; }

execute_success() {
  local name="$1" operation="$2"
  "$provision" deployment execute --plan "$plan_id" --operation "$operation" --state "$state" --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/$name/$operation.json"
  json_assert "$work_dir/$name/$operation.json" outcome '"succeeded"'
}

plan_generation() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["keyValue"]["generationId"])' "$1"; }

switch_marker() { echo "$generations_root/$transition_generation/transition/authority-switch.json"; }

container_started_at() (
  cd / && sudo -n runuser -u provision-lab -- env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR="/run/user/$(id -u provision-lab)" \
    podman inspect --format '{{.State.StartedAt}}' provision-lab-valkey
)

execute_with_interruption_and_resume() {
  local name="$1" operation="$2"
  local kind marker replay_marker result lease resume_delay mode="$fault_mode" undecidable=""
  if [[ "$fault_mode" == undecidable ]]; then
    mode=after
    [[ "$(plan_kind "$work_dir/$name/plan.json" "$operation")" == switchKeyValueAuthority ]] && undecidable=1
  fi
  kind="$(plan_kind "$work_dir/$name/plan.json" "$operation")"
  marker="$work_dir/$name/$operation-$mode-marker"
  lease=5s resume_delay=6
  [[ "$mode" == after ]] && { lease=30s; resume_delay=31; }
  set +e
  PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE="$mode" PROVISION_FAULT_MARKER="$marker" \
    "$provision" deployment execute --plan "$plan_id" --operation "$operation" --state "$state" --signing-key "$signing_key" \
      --secret-file "$secret_reference=$secret_file" --lease-duration "$lease" >"$work_dir/$name/$operation-$mode.txt" 2>&1
  result=$?
  set -e
  [[ $result -ne 0 && -f "$marker" ]] || fail "$operation ($kind) did not inject $mode interruption"
  write_status "$name" "$operation-$mode"
  journal_assert_latest "$work_dir/$name/$operation-$mode-status.json" "$operation" intent -
  journal_assert_paused "$work_dir/$name/$operation-$mode-status.json" "$operation"
  sleep "$resume_delay"
  local injection="$mode"
  [[ -n "$undecidable" ]] && injection=undecidable
  if [[ -n "$undecidable" ]]; then
    # An unreadable authority-switch marker (a directory in its place) means resume cannot tell
    # whether authority moved; it must record uncertainty and must not replay the switch.
    local evidence started_before started_after
    evidence="$(switch_marker)"
    started_before="$(container_started_at)"
    sudo mv "$evidence" "$evidence.orig"
    sudo mkdir "$evidence"
    set +e
    "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" \
      --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/$name/$operation-undecidable.txt" 2>&1
    result=$?
    set -e
    sudo rmdir "$evidence"
    sudo mv "$evidence.orig" "$evidence"
    [[ $result -ne 0 ]] || fail "$operation resumed despite an undecidable authority-switch observation"
    assert_contains "$work_dir/$name/$operation-undecidable.txt" "requires explicit recovery because its outcome is uncertain"
    started_after="$(container_started_at)"
    [[ -n "$started_before" && "$started_before" == "$started_after" ]] || fail "$operation resume restarted the authoritative container ($started_before -> $started_after): the switch was replayed"
    printf '%s\n' "$started_after" >"$work_dir/$name/$operation-container-started-at.txt"
    write_status "$name" "$operation-undecidable"
    journal_assert_latest "$work_dir/$name/$operation-undecidable-status.json" "$operation" outcome uncertain
    assert_contains "$work_dir/$name/$operation-undecidable-status.json" "recoveryAction"
    python3 - "$work_dir/$name/$operation-undecidable.txt" "$work_dir/$name/$operation-uncertain.json" "$started_before" "$started_after" <<'PYEOF'
import json, sys
text = open(sys.argv[1], encoding="utf-8").read()
result, _ = json.JSONDecoder().raw_decode(text)
key_value = result["observation"]["keyValue"]
json.dump({
    "outcome": result["outcome"], "reason": key_value["reason"], "recoveryAction": key_value["recoveryAction"],
    "failureCategory": result["observation"].get("failureCategory"),
    "authoritativeContainerStartedAtUnchanged": sys.argv[3] == sys.argv[4],
}, open(sys.argv[2], "w", encoding="utf-8"), indent=2, sort_keys=True)
PYEOF
  fi
  if [[ "$mode" == after ]]; then
    replay_marker="$work_dir/$name/$operation-unexpected-replay"
    PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$replay_marker" \
      "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" \
        --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/$name/$operation-resumed.json"
    [[ ! -e "$replay_marker" ]] || fail "$operation resume replayed a completed $kind host mutation"
  else
    "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" \
      --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/$name/$operation-resumed.json"
  fi
  json_assert "$work_dir/$name/$operation-resumed.json" outcome '"succeeded"'
  write_status "$name" "$operation-resumed"
  journal_assert_resume "$work_dir/$name/$operation-resumed-status.json" "$operation" succeeded
  python3 - "$work_dir/$name/$operation-fault.json" "$kind" "$injection" "$mode" "$([[ -e "${replay_marker:-}" ]] && echo true || echo false)" <<'PYEOF'
import json, sys
path, kind, injection, interruption, replayed = sys.argv[1:]
json.dump({"kind": kind, "injection": injection, "interruption": interruption,
           "pausedWithAttemptAndFencingEvidence": True, "mutationReplayedOnResume": replayed == "true",
           "resumeOutcome": "succeeded"}, open(path, "w", encoding="utf-8"), indent=2, sort_keys=True)
PYEOF
}

assert_keys() {
  local label="$1" plan="$2" uid value key count=0
  uid="$(id -u provision-lab)"
  : >"$work_dir/$label-keys.txt"
  while read -r key; do
    value="$(cd / && sudo -n runuser -u provision-lab -- env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR=/run/user/"$uid" podman exec provision-lab-valkey /tmp/provision-cli.sh GET "$key" | tr -d '\r')"
    [[ "$value" == "provision-deterministic-v1:$key" ]] || fail "$label key $key = $value"
    printf '%s %s\n' "$key" "$value" >>"$work_dir/$label-keys.txt"
    count=$((count + 1))
  done < <(python3 -c 'import json,sys; print("\n".join(json.load(open(sys.argv[1]))["keyValue"]["binding"]["keys"]))' "$plan")
  [[ $count -gt 0 ]] || fail "$label plan declares no deterministic keys"
}

assert_active() {
  local label="$1" version="$2" generation="$3"
  "$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/$label-host.json"
  json_assert "$work_dir/$label-host.json" ready true
  json_assert "$work_dir/$label-host.json" keyValue.active.id "\"$generation\""
  json_assert "$work_dir/$label-host.json" keyValue.active.valkeyVersion "\"$version\""
  json_assert "$work_dir/$label-host.json" keyValue.active.health '"healthy"'
  json_assert "$work_dir/$label-host.json" keyValue.active.connectivity true
  json_assert "$work_dir/$label-host.json" keyValue.active.port 26379
}

set_environment_version() {
  python3 - "$config_dir/environment.yaml" "$1" <<'PY'
from pathlib import Path
import sys
path, version = Path(sys.argv[1]), sys.argv[2]
out = []
for line in path.read_text(encoding="utf-8").splitlines():
    if line.strip().startswith("version:"):
        continue
    out.append(line)
    if line.strip() == "credential: secret://lab/valkey-url":
        out.append(line[: len(line) - len(line.lstrip())] + f'version: "{version}"')
path.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
}

cp "$repo_root/examples/host-key-value/"{application,revision,root}.yaml "$config_dir/"
python3 - "$repo_root/examples/host-key-value/environment.yaml" "$config_dir/environment.yaml" "$operator" <<'PY'
import sys
from pathlib import Path
src, dst, operator = sys.argv[1:]
out = []
for line in Path(src).read_text(encoding="utf-8").splitlines():
    s = line.strip()
    if s.startswith("address:"):
        out.append("    local: true")
    elif s.startswith("user:"):
        out.append(f"    user: {operator}")
    else:
        out.append(line)
Path(dst).write_text("\n".join(out) + "\n", encoding="utf-8")
PY
set_environment_version "$initial_version"

"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/initial-host.json"
json_assert "$work_dir/initial-host.json" ready true
if find "$generations_root" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null | grep -q .; then
  fail "Valkey generation storage is not empty; reset the disposable host before running the matrix"
fi

echo "[1/4] CLI-provisioned initial Key-Value Store generation ($initial_version)"
preview_and_approve initial
initial_generation="$(plan_generation "$work_dir/initial/plan.json")"
for operation in $(plan_operations "$work_dir/initial/plan.json"); do execute_success initial "$operation"; done
assert_active initial-final "$initial_version" "$initial_generation"
assert_keys initial "$work_dir/initial/plan.json"

echo "[2/4] retarget the Key-Value Store at Valkey $transition_version and approve the Store Transition"
set_environment_version "$transition_version"
preview_and_approve transition
transition_generation="$(plan_generation "$work_dir/transition/plan.json")"
[[ "$initial_generation" != "$transition_generation" ]] || fail "transition Plan targets the active generation"
[[ "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["operations"][0]["input"]["keyValue"]["previous"]["id"])' "$work_dir/transition/plan.json")" == "$initial_generation" ]] || fail "transition Plan does not replace the CLI-provisioned generation"
expected_kinds="prepareKeyValueCandidate synchronizeKeyValueCandidate fenceKeyValueWrites switchKeyValueAuthority verifyKeyValueActive retainKeyValuePrevious"
actual_kinds=""
for operation in $(plan_operations "$work_dir/transition/plan.json"); do actual_kinds="${actual_kinds:+$actual_kinds }$(plan_kind "$work_dir/transition/plan.json" "$operation")"; done
[[ "$actual_kinds" == "$expected_kinds" ]] || fail "unexpected transition operations: $actual_kinds"

echo "[3/4] Store Transition with $fault_mode interruption around every operation"
for operation in $(plan_operations "$work_dir/transition/plan.json"); do
  execute_with_interruption_and_resume transition "$operation"
done
json_assert "$work_dir/transition/op-03-resumed.json" observation.writeFence '"acl-write-revocation-with-client-kill"'
json_assert "$work_dir/transition/op-04-resumed.json" observation.transitionPhase '"switchKeyValueAuthority"'
json_assert "$work_dir/transition/op-05-resumed.json" observation.transitionPhase '"verifyKeyValueActive"'
json_assert "$work_dir/transition/op-06-resumed.json" observation.transitionPhase '"retainKeyValuePrevious"'
json_assert "$work_dir/transition/op-06-resumed.json" observation.previous.id "\"$initial_generation\""
assert_active transition-final "$transition_version" "$transition_generation"
assert_keys transition "$work_dir/transition/plan.json"

echo "[4/4] accounting and evidence redaction"
password="$(sed -E 's#^valkey://[^:]+:([^@]+)@.*#\1#' "$secret_file" | tr -d '\n')"
[[ -n "$password" ]]
if grep -R --binary-files=without-match -F "$password" "$work_dir" >/dev/null 2>&1; then fail "resolved Valkey secret appeared in evidence"; fi
# shellcheck disable=SC2024 # The inventory is written by this user; only find needs privilege.
sudo find /var/lib/provision/environments/lab/services /etc/containers/systemd/users -maxdepth 4 -printf '%y %m %u:%g %p\n' >"$work_dir/owned-inventory.txt"
"$script_dir/valkey-matrix-accounting.py" "$work_dir" "$work_dir/owned-inventory.txt" >"$work_dir/accounting.json"
python3 - "$work_dir" "$provision" "$provision_version" "$actual_host" "$(uname -sr)" "$fault_mode" <<'PY'
import hashlib, json, sys
from pathlib import Path
work, provision, version, host, kernel, mode = sys.argv[1:]
w = Path(work)
initial = json.loads((w / "initial-host.json").read_text())
final = json.loads((w / "transition-final-host.json").read_text())
summary = {
    "schemaVersion": "provision.dev/direct-local-valkey-failure-matrix/v1alpha1",
    "result": "passed", "faultMode": mode,
    "provisionSourceVersion": version,
    "provisionBinarySha256": "sha256:" + hashlib.sha256(Path(provision).read_bytes()).hexdigest(),
    "host": host, "kernel": kernel, "os": initial["os"], "osVersion": initial["osVersion"],
    "architecture": initial["architecture"], "systemdVersion": initial["systemdVersion"],
    "executorDigest": final["executorDigest"],
    "accounting": json.loads((w / "accounting.json").read_text()),
    "supportBoundaries": [
        "single Host Target", "forward-only rollback classification",
        "authoritative Store Data Role only", "same-major transition only",
        "no crash-durability, host-loss, backup or restore claim",
    ],
}
(w / "support-observation.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
PY
echo "direct-local Valkey $fault_mode matrix passed"
