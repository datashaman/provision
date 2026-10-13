#!/usr/bin/env bash
# Destructive direct-local boundary proofs for the managed Valkey Store Transition (issue #102).
#   candidate-failure: a candidate stopped after preparation fails synchronization and never disturbs
#                      the active generation, its data or the workload keys; no authority-switch
#                      evidence appears and a direct authority switch is refused.
#   forward-only:      a write accepted after cutover exists only in the new generation, absent from the
#                      retained previous generation, so the forward-only classification is honest.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME --scenario candidate-failure|forward-only" >&2
  exit 2
}

provision="" signing_key="" secret_file="" work_dir="" provision_version="" confirmed_host="" scenario=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
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
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$scenario" == candidate-failure || "$scenario" == forward-only ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local Valkey boundaries require Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "operator identity is unsupported" >&2; exit 1; }
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
actual_host="$(hostname)"
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }
command -v python3 >/dev/null
sudo -n true

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
# shellcheck source=lib/matrix-helpers.sh
source "$script_dir/lib/matrix-helpers.sh"
provision="$(abs "$provision")"; signing_key="$(abs "$signing_key")"; secret_file="$(abs "$secret_file")"
state="$work_dir/state.db"
secret_reference="secret://lab/valkey-url"
initial_version=9.1.1 transition_version=9.1.2
generations_root=/var/lib/provision/environments/lab/services/valkey/generations
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

fail() { echo "Valkey boundaries ($scenario): $*" >&2; exit 1; }

execute() {
  "$provision" deployment execute --plan "$plan_id" --operation "$2" --state "$state" --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/$1/$2.json" 2>"$work_dir/$1/$2.err"
}
execute_success() { execute "$1" "$2" || fail "$2 did not succeed: $(cat "$work_dir/$1/$2.err")"; json_assert "$work_dir/$1/$2.json" outcome '"succeeded"'; }
env_run() (
  local uid
  uid="$(id -u provision-lab)"
  cd / && sudo -n runuser -u provision-lab -- env HOME=/var/lib/provision/runtime/lab "XDG_RUNTIME_DIR=/run/user/$uid" "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus" "$@"
)
vk() { env_run podman exec provision-lab-valkey /tmp/provision-cli.sh "$@" | tr -d '\r'; }

assert_active() {
  local label="$1" version="$2" generation="$3"
  "$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/$label-host.json"
  json_assert "$work_dir/$label-host.json" ready true
  json_assert "$work_dir/$label-host.json" keyValue.active.id "\"$generation\""
  json_assert "$work_dir/$label-host.json" keyValue.active.valkeyVersion "\"$version\""
  json_assert "$work_dir/$label-host.json" keyValue.active.health '"healthy"'
  json_assert "$work_dir/$label-host.json" keyValue.active.connectivity true
}

assert_keys() {
  local label="$1" plan="$2" key count=0
  while read -r key; do
    [[ "$(vk GET "$key")" == "provision-deterministic-v1:$key" ]] || fail "$label key $key lost or changed"
    count=$((count + 1))
  done < <(python3 -c 'import json,sys; print("\n".join(json.load(open(sys.argv[1]))["keyValue"]["binding"]["keys"]))' "$plan")
  [[ $count -gt 0 ]] || fail "$label plan declares no deterministic keys"
}

cp "$repo_root/examples/host-key-value/"{application,revision,root}.yaml "$config_dir/"
python3 - "$repo_root/examples/host-key-value/environment.yaml" "$config_dir/environment.yaml" "$operator" <<'PY'
import sys
from pathlib import Path
src, dst, operator = sys.argv[1:]
out = []
for line in Path(src).read_text(encoding="utf-8").splitlines():
    s = line.strip()
    out.append("    local: true" if s.startswith("address:") else f"    user: {operator}" if s.startswith("user:") else line)
Path(dst).write_text("\n".join(out) + "\n", encoding="utf-8")
PY
set_environment_version "$initial_version"

if sudo -n find "$generations_root" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null | grep -q .; then
  fail "Valkey generation storage is not empty; reset the disposable host before running"
fi

echo "[1/4] initial generation and an acknowledged pre-transition write"
preview_and_approve initial
initial_generation="$(plan_generation "$work_dir/initial/plan.json")"
for operation in $(plan_operations "$work_dir/initial/plan.json"); do execute_success initial "$operation"; done
assert_active initial-final "$initial_version" "$initial_generation"
assert_keys initial "$work_dir/initial/plan.json"
[[ "$(vk SET boundary:pre-transition acknowledged)" == OK ]] || fail "pre-transition write was not acknowledged"

echo "[2/4] retarget at Valkey $transition_version and approve the Store Transition"
set_environment_version "$transition_version"
preview_and_approve transition
transition_generation="$(plan_generation "$work_dir/transition/plan.json")"
[[ "$transition_generation" != "$initial_generation" ]] || fail "transition Plan targets the active generation"
read -r -a ops <<<"$(plan_operations "$work_dir/transition/plan.json")"
[[ ${#ops[@]} -eq 6 ]] || fail "expected six transition operations, got ${#ops[@]}"
json_assert "$work_dir/transition/plan.json" keyValue.transition.storeRollbackGuarantee '"forward-only"'
switch_evidence="$generations_root/$transition_generation/transition/authority-switch.json"

if [[ "$scenario" == candidate-failure ]]; then
  echo "[3/4] candidate is prepared, then stopped before synchronization"
  execute_success transition "${ops[0]}"
  candidate_unit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["keyValue"]["transition"]["candidateServiceUnit"])' "$work_dir/transition/plan.json")"
  env_run systemctl --user stop "$candidate_unit"
  ! execute transition "${ops[1]}" || fail "synchronization succeeded although the candidate was stopped"
  write_status transition after-candidate-failure
  python3 - "$work_dir/transition/after-candidate-failure-status.json" "${ops[1]}" <<'PY'
import json, sys
outcomes = [e.get("outcome") for e in json.load(open(sys.argv[1], encoding="utf-8"))["events"]
            if e["operationId"] == sys.argv[2] and e["kind"] == "outcome"]
if outcomes[-1:] != ["failed"]:
    raise SystemExit(f"synchronization must end failed, got {outcomes!r}")
PY
  echo "[4/4] the active generation is untouched and the switch cannot proceed"
  check_untouched() {
    assert_active "$1" "$initial_version" "$initial_generation"
    assert_keys "$1" "$work_dir/initial/plan.json"
    [[ "$(vk GET boundary:pre-transition)" == acknowledged ]] || fail "$1: active data changed"
    sudo -n test ! -e "$switch_evidence" || fail "$1: authority-switch evidence exists"
  }
  check_untouched candidate-failed
  ! execute transition "${ops[3]}" || fail "authority switch ran although the candidate never synchronized"
  assert_contains "$work_dir/transition/${ops[3]}.err" "operation dependency ${ops[2]} has no recorded outcome"
  check_untouched candidate-failed-after-switch-attempt
  echo "candidate-failure boundary proved"
else
  echo "[3/4] complete the forward-only Store Transition"
  for operation in "${ops[@]}"; do execute_success transition "$operation"; done
  assert_active transition-final "$transition_version" "$transition_generation"
  assert_keys transition "$work_dir/transition/plan.json"
  [[ "$(vk GET boundary:pre-transition)" == acknowledged ]] || fail "pre-transition write lost in the new generation"

  echo "[4/4] a post-cutover write exists only in the new generation"
  [[ "$(vk SET boundary:post-cutover acknowledged)" == OK ]] || fail "post-cutover write was not acknowledged"
  [[ "$(vk GET boundary:post-cutover)" == acknowledged ]] || fail "new generation lacks its own write"
  previous_data="$generations_root/$initial_generation/data"
  sudo -n test -d "$previous_data" || fail "previous generation data was not retained"
  sudo -n grep -rqF boundary:pre-transition "$previous_data" || fail "retained previous generation lacks the pre-transition write"
  if sudo -n grep -rqF boundary:post-cutover "$previous_data"; then fail "retained previous generation holds the post-cutover write"; fi
  json_assert "$work_dir/transition/${ops[5]}.json" observation.previous.id "\"$initial_generation\""
  json_assert "$work_dir/transition/${ops[5]}.json" observation.synchronization '"retained-forward-only"'
  echo "forward-only boundary proved: the retained generation lacks the acknowledged post-cutover write"
fi
password="$(sed -E 's#^valkey://[^:]+:([^@]+)@.*#\1#' "$secret_file" | tr -d '\n')"
[[ -n "$password" ]] || fail "could not derive the secret to scan for"
if grep -R --binary-files=without-match -F "$password" "$work_dir" >/dev/null 2>&1; then fail "resolved Valkey secret appeared in evidence"; fi
echo "evidence: $work_dir"
