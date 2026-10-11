#!/usr/bin/env bash
# Destructive direct-local acceptance for a Key-Value-bound HTTP workload
# (issue #97). Runs on the disposable Host Target as its bootstrap operator.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --artifact-bundle FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME" >&2
  exit 2
}

provision="" signing_key="" secret_file="" artifact_bundle="" work_dir="" provision_version="" confirmed_host=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --artifact-bundle) (($# >= 2)) || usage; artifact_bundle="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    *) usage ;;
  esac
done
for f in "$provision" "$signing_key" "$secret_file" "$artifact_bundle"; do
  [[ -n "$f" && -f "$f" && ! -L "$f" ]] || usage
done
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$(uname -s)" == Linux && "$(id -u)" != 0 ]] || { echo "requires Linux and the non-root bootstrap operator" >&2; exit 1; }
operator="$(id -un)"
[[ "$confirmed_host" == "$(hostname)" ]] || { echo "disposable-host confirmation does not match $(hostname)" >&2; exit 1; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
command -v curl >/dev/null; command -v python3 >/dev/null; sudo -n true

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
abs() { echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }
provision="$(abs "$provision")"; signing_key="$(abs "$signing_key")"; secret_file="$(abs "$secret_file")"; artifact_bundle="$(abs "$artifact_bundle")"
state="$work_dir/state.db"
secret_reference="secret://lab/valkey-url"
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"
fail() { echo "direct-local Valkey HTTP acceptance: $*" >&2; exit 1; }

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

cp "$repo_root/examples/host-key-value-http/"{application,root}.yaml "$config_dir/"
digest="sha256:$(sha256sum "$artifact_bundle" | awk '{print $1}')"
sudo install -d -o root -g root -m 0755 /var/lib/provision/artifacts/sha256
sudo install -o root -g root -m 0644 "$artifact_bundle" "/var/lib/provision/artifacts/sha256/${digest#sha256:}"
python3 - "$repo_root/examples/host-key-value-http" "$config_dir" "$operator" "$digest" <<'PY'
import sys
from pathlib import Path
src, dst, operator, digest = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3], sys.argv[4]
env = []
for line in (src / "environment.yaml").read_text(encoding="utf-8").splitlines():
    s = line.strip()
    if s.startswith("address:"):
        env.append("    local: true")
    elif s.startswith("user:"):
        env.append(f"    user: {operator}")
    else:
        env.append(line)
(dst / "environment.yaml").write_text("\n".join(env) + "\n", encoding="utf-8")
rev = []
for line in (src / "revision.yaml").read_text(encoding="utf-8").splitlines():
    s = line.strip()
    if s.startswith("source:"):
        rev.append("    source: https://artifacts.invalid/valkey-http-fixture.tar.gz")
    elif s.startswith("digest:"):
        rev.append(f"    digest: {digest}")
    else:
        rev.append(line)
(dst / "revision.yaml").write_text("\n".join(rev) + "\n", encoding="utf-8")
PY

if curl --silent --max-time 1 http://127.0.0.1:18080/verify >/dev/null 2>&1; then fail "stable Endpoint already exists; reset the disposable host"; fi

echo "[1/4] validate, preview, approve"
"$provision" config validate --file "$config_dir/root.yaml" >"$work_dir/validation.json"
"$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$work_dir/plan.json"
plan_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$work_dir/plan.json")"
[[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity"
json_assert "$work_dir/plan.json" operations.0.kind '"prepareKeyValue"'
json_assert "$work_dir/plan.json" operations.5.kind '"switchEndpoint"'
"$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$operator" --state "$state" --expires-after 2h >"$work_dir/approval.json"
json_assert "$work_dir/approval.json" eligible true

echo "[2/4] execute the store, then the bound workload"
count="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["operations"]))' "$work_dir/plan.json")"
for ((i = 1; i <= count; i++)); do
  op="$(printf 'op-%02d' "$i")"
  "$provision" deployment execute --plan "$plan_id" --operation "$op" --state "$state" --signing-key "$signing_key" \
    --secret-file "$secret_reference=$secret_file" --lease-duration 2m >"$work_dir/$op.json"
  json_assert "$work_dir/$op.json" outcome '"succeeded"'
done

echo "[3/4] the stable Endpoint proves the workload bound by logical reference"
curl --silent --fail --max-time 10 http://127.0.0.1:18080/verify >"$work_dir/endpoint-verify.json"
json_assert "$work_dir/endpoint-verify.json" keyValueBinding.status '"verified"'
json_assert "$work_dir/endpoint-verify.json" keyValueBinding.logicalId '"provision-lab-sessions"'
json_assert "$work_dir/endpoint-verify.json" keyValueBinding.keys.0.value '"provision-deterministic-v1:provision-lab-sessions/provision-example-key-value-http-v1/key-0001"'
"$provision" host bootstrap check --local --environment lab --operator "$operator" >"$work_dir/final-host.json"
json_assert "$work_dir/final-host.json" keyValue.active.health '"healthy"'

echo "[4/4] no resolved credential appears in plans, journals, status, or evidence"
password="$(sed -E 's#^valkey://[^:]+:([^@]+)@.*#\1#' "$secret_file" | tr -d '\n')"
[[ -n "$password" ]]
"$provision" deployment status --plan "$plan_id" --state "$state" >"$work_dir/status.json"
if grep -R --binary-files=without-match -F "$password" "$work_dir" >/dev/null 2>&1; then fail "resolved Valkey secret appeared in evidence"; fi
if sudo grep -R --binary-files=without-match -F "$password" /etc/systemd /etc/containers/systemd /var/lib/provision/environments/lab/releases >/dev/null 2>&1; then fail "resolved Valkey secret appeared in a unit or release directory"; fi
printf '{"schemaVersion":"provision.dev/direct-local-valkey-http-acceptance/v1alpha1","provisionVersion":"%s","host":"%s","plan":"%s","passed":true}\n' "$provision_version" "$(hostname)" "$plan_id" >"$work_dir/summary.json"
echo "direct-local Valkey HTTP acceptance passed"
