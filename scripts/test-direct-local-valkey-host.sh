#!/usr/bin/env bash
# Destructive direct-local acceptance for the first managed Valkey generation
# (issue #97). Runs on the disposable Host Target as its bootstrap operator.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME [--fault-mode none|before|after]" >&2
  exit 2
}

provision="" signing_key="" secret_file="" work_dir="" provision_version="" confirmed_host="" fault_mode=none
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
[[ "$fault_mode" == none || "$fault_mode" == before || "$fault_mode" == after ]] || usage
[[ "$(uname -s)" == Linux ]] || { echo "direct-local Valkey acceptance requires Linux" >&2; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as the bootstrapped non-root operator, not root" >&2; exit 1; }
operator="$(id -un)"
[[ "$confirmed_host" == "$(hostname)" ]] || { echo "disposable-host confirmation does not match $(hostname)" >&2; exit 1; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fault_bin="$repo_root/scripts/direct-local-fault-bin"
command -v python3 >/dev/null
sudo -n true

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
secret_file="$(cd "$(dirname "$secret_file")" && pwd)/$(basename "$secret_file")"
state="$work_dir/state.db"
secret_reference="secret://lab/valkey-url"
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

fail() { echo "direct-local Valkey acceptance: $*" >&2; exit 1; }

json_assert() {
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
path, field, expected = sys.argv[1:]
value = json.load(open(path, encoding="utf-8"))
for segment in field.split("."):
    value = value[int(segment)] if isinstance(value, list) else value[segment]
if value != json.loads(expected):
    raise SystemExit(f"{path}: {field} = {value!r}, expected {expected}")
PY
}

cp "$repo_root/examples/host-key-value/"{application,revision,root}.yaml "$config_dir/"
python3 - "$repo_root/examples/host-key-value/environment.yaml" "$config_dir/environment.yaml" "$operator" <<'PY'
import sys
from pathlib import Path
out = []
for line in Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    s = line.strip()
    if s.startswith("address:"):
        out.append("    local: true")
    elif s.startswith("user:"):
        out.append(f"    user: {sys.argv[3]}")
    else:
        out.append(line)
Path(sys.argv[2]).write_text("\n".join(out) + "\n", encoding="utf-8")
PY

"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/initial-host.json"
json_assert "$work_dir/initial-host.json" ready true
json_assert "$work_dir/initial-host.json" keyValue.capabilities.appendFsync '"everysec"'
json_assert "$work_dir/initial-host.json" keyValue.capabilities.port 26379

echo "[1/5] validate, preview, approve"
"$provision" config validate --file "$config_dir/root.yaml" >"$work_dir/validation.json"
"$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$work_dir/plan.json"
json_assert "$work_dir/plan.json" operations.0.kind '"prepareKeyValue"'
plan_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$work_dir/plan.json")"
[[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity"
"$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$operator" --state "$state" --expires-after 2h >"$work_dir/approval.json"
json_assert "$work_dir/approval.json" eligible true

execute_args=(--plan "$plan_id" --operation op-01 --state "$state" --signing-key "$signing_key" --secret-file "$secret_reference=$secret_file")
echo "[2/5] execute prepareKeyValue (fault mode: $fault_mode)"
if [[ "$fault_mode" == none ]]; then
  "$provision" deployment execute "${execute_args[@]}" --lease-duration 2m >"$work_dir/op-01.json"
else
  marker="$work_dir/fault-marker"
  lease=5s delay=6
  [[ "$fault_mode" == after ]] && lease=30s delay=31
  set +e
  PATH="$fault_bin:$PATH" PROVISION_FAULT_MODE="$fault_mode" PROVISION_FAULT_MARKER="$marker" \
    "$provision" deployment execute "${execute_args[@]}" --lease-duration "$lease" >"$work_dir/op-01-interrupted.txt" 2>&1
  result=$?
  set -e
  [[ $result -ne 0 && -f "$marker" ]] || fail "prepareKeyValue did not inject $fault_mode interruption"
  sleep "$delay"
  "$provision" deployment resume --plan "$plan_id" --state "$state" --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/op-01.json"
fi
json_assert "$work_dir/op-01.json" outcome '"succeeded"'

echo "[3/5] inspection reports the exact observed generation"
"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/final-host.json"
json_assert "$work_dir/final-host.json" keyValue.active.health '"healthy"'
json_assert "$work_dir/final-host.json" keyValue.active.connectivity true
json_assert "$work_dir/final-host.json" keyValue.active.valkeyVersion '"9.1.2"'
json_assert "$work_dir/final-host.json" keyValue.active.appendOnly '"yes"'
json_assert "$work_dir/final-host.json" keyValue.active.appendFsync '"everysec"'
json_assert "$work_dir/final-host.json" keyValue.active.listenAddress '"127.0.0.1"'
json_assert "$work_dir/final-host.json" keyValue.active.port 26379

echo "[4/5] deterministic keys are readable from the active generation"
uid="$(id -u provision-lab)"
for key in $(python3 -c 'import json,sys; print(" ".join(json.load(open(sys.argv[1]))["keyValue"]["binding"]["keys"]))' "$work_dir/plan.json"); do
  value="$(cd / && sudo runuser -u provision-lab -- env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR="/run/user/$uid" \
    podman exec provision-lab-valkey /tmp/provision-cli.sh GET "$key" | tr -d '\r')"
  [[ "$value" == "provision-deterministic-v1:$key" ]] || fail "deterministic key $key = $value"
done

echo "[5/5] no resolved credential appears in plans, journals, status, or evidence"
password="$(sed -E 's#^valkey://[^:]+:([^@]+)@.*#\1#' "$secret_file" | tr -d '\n')"
[[ -n "$password" ]]
"$provision" deployment status --plan "$plan_id" --state "$state" >"$work_dir/status.json"
if grep -R --binary-files=without-match -F "$password" "$work_dir" >/dev/null 2>&1; then
  fail "resolved Valkey secret appeared in evidence"
fi
printf '{"schemaVersion":"provision.dev/direct-local-valkey-acceptance/v1alpha1","provisionVersion":"%s","host":"%s","faultMode":"%s","plan":"%s","passed":true}\n' \
  "$provision_version" "$(hostname)" "$fault_mode" "$plan_id" >"$work_dir/summary.json"
echo "direct-local Valkey acceptance ($fault_mode) passed"
