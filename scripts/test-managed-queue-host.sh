#!/usr/bin/env bash
# Black-box acceptance for the first managed Host Queue. The target must have
# been prepared with scripts/bootstrap-host.sh and must be disposable.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --work-dir DIRECTORY --target HOST --target-user USER [--seed-state FILE]" >&2
  exit 2
}

provision=""
signing_key=""
config=""
secret_file=""
work_dir=""
target=""
target_user=""
seed_state=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --config) (($# >= 2)) || usage; config="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
    --seed-state) (($# >= 2)) || usage; seed_state="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -x "$provision" && -f "$signing_key" && -f "$config" && -f "$secret_file" ]] || usage
[[ "$work_dir" == /* && ! -e "$work_dir" ]] || usage
[[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage
[[ "$(stat -f '%Lp' "$secret_file" 2>/dev/null || stat -c '%a' "$secret_file")" =~ ^[46]00$ ]] || {
  echo "secret file must be private (0400 or 0600)" >&2
  exit 1
}

mkdir -m 0700 "$work_dir"
state="$work_dir/state.db"
second_state="$work_dir/reexecution-state.db"
secret_reference="secret://lab/rabbitmq-url"
if [[ -n "$seed_state" ]]; then
  [[ -f "$seed_state" && ! -L "$seed_state" ]] || usage
  install -m 0600 "$seed_state" "$state"
fi

plan_id() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' < "$1"
}

assert_secret_absent() {
  local password
  password="$(sed -E 's#^[^:]+://[^:]+:([^@]+)@.*#\1#' "$secret_file")"
  [[ -n "$password" ]] || { echo "resolved Queue secret has no password" >&2; exit 1; }
  if grep -R -F -- "$password" "$work_dir" >/dev/null 2>&1; then
    echo "resolved Queue credential leaked into durable acceptance output" >&2
    exit 1
  fi
}

echo "[1/6] inspect qualified Queue bootstrap"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-before.json"

echo "[2/6] preview and approve Queue-only Plan"
"$provision" plan preview --file "$config" --state "$state" > "$work_dir/plan.json"
first_plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$first_plan" --actor "$(id -un)" --state "$state" \
  > "$work_dir/approval.json"

echo "[3/6] execute signed prepareQueue operation"
"$provision" deployment execute \
  --plan "$first_plan" --operation op-01 --state "$state" --signing-key "$signing_key" \
  --secret-file "$secret_reference=$secret_file" --lease-duration 5m \
  > "$work_dir/execution.json"
"$provision" deployment status --plan "$first_plan" --state "$state" > "$work_dir/status.json"

echo "[4/6] inspect exact managed Queue generation"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-after.json"
grep -Fq '"health": "healthy"' "$work_dir/bootstrap-after.json"
grep -Fq '"accepted": 1' "$work_dir/bootstrap-after.json"
grep -Fq '"acknowledged": 1' "$work_dir/bootstrap-after.json"
grep -Fq '"deliveryLimit": 3' "$work_dir/bootstrap-after.json"

echo "[5/6] re-plan and observe exact existing generation without duplication"
"$provision" plan preview --file "$config" --state "$second_state" > "$work_dir/reexecution-plan.json"
second_plan="$(plan_id "$work_dir/reexecution-plan.json")"
"$provision" plan approve --file "$config" --plan "$second_plan" --actor "$(id -un)" --state "$second_state" \
  > "$work_dir/reexecution-approval.json"
"$provision" deployment execute \
  --plan "$second_plan" --operation op-01 --state "$second_state" --signing-key "$signing_key" \
  --secret-file "$secret_reference=$secret_file" --lease-duration 5m \
  > "$work_dir/reexecution.json"
cmp -s "$work_dir/execution.json" "$work_dir/reexecution.json" || {
  python3 - "$work_dir/execution.json" "$work_dir/reexecution.json" <<'PY'
import json
import sys

left = json.load(open(sys.argv[1], encoding="utf-8"))["observation"]
right = json.load(open(sys.argv[2], encoding="utf-8"))["observation"]
if left != right:
    raise SystemExit("re-execution did not observe the same exact Queue generation")
PY
}

echo "[6/6] verify secret redaction and durable journal"
"$provision" deployment status --plan "$second_plan" --state "$second_state" > "$work_dir/reexecution-status.json"
assert_secret_absent
grep -Fq '"outcome": "succeeded"' "$work_dir/status.json"
grep -Fq '"outcome": "succeeded"' "$work_dir/reexecution-status.json"

echo "managed Queue acceptance passed"
echo "evidence: $work_dir"
