#!/usr/bin/env bash
# Black-box acceptance for the first managed Host Queue. The target must have
# been prepared with scripts/bootstrap-host.sh and must be disposable.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
fault_bin="$repo_root/scripts/remote-ssh-fault-bin"

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
[[ -x "$fault_bin/ssh" ]] || { echo "SSH response-loss helper is missing" >&2; exit 1; }
[[ "$(stat -f '%Lp' "$secret_file" 2>/dev/null || stat -c '%a' "$secret_file")" =~ ^[46]00$ ]] || {
  echo "secret file must be private (0400 or 0600)" >&2
  exit 1
}

mkdir -m 0700 "$work_dir"
state="$work_dir/state.db"
second_state="$work_dir/reexecution-state.db"
secret_reference="secret://lab/rabbitmq-url"
real_ssh="$(command -v ssh)"
ssh_options=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ConnectTimeout=5 -- "$target_user@$target")
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

remote() {
  "$real_ssh" "${ssh_options[@]}" "$1"
}

capture_unrelated_inventory() {
  local prefix="$1"
  remote "systemctl list-unit-files --no-legend --no-pager | sort" > "$work_dir/$prefix-system-units.txt"
  remote "dpkg-query -W -f='\${binary:Package}\t\${Version}\n' | sort" > "$work_dir/$prefix-packages.txt"
  remote "find /etc/containers/systemd/users -type f ! -name 'provision-lab-rabbitmq.container' -printf '%p\n' 2>/dev/null | sort" > "$work_dir/$prefix-unrelated-quadlets.txt"
}

assert_resume_provenance() {
  python3 - "$1" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    status = json.load(handle)
events = [event for event in status["events"] if event["operationId"] == "op-01"]
if not any(event["kind"] == "intent" and event.get("observation", {}).get("resumeOfAttemptId") for event in events):
    raise SystemExit("Queue recovery has no durable resume provenance")
if events[-1]["kind"] != "outcome" or events[-1].get("outcome") != "succeeded":
    raise SystemExit("Queue recovery did not finish with a successful outcome")
PY
}

capture_unrelated_inventory before

echo "[1/7] inspect qualified Queue bootstrap"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-before.json"

echo "[2/7] preview and approve declared Queue-only Plan"
"$provision" plan preview --file "$config" --state "$state" > "$work_dir/plan.json"
first_plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$first_plan" --actor "$(id -un)" --state "$state" \
  > "$work_dir/approval.json"

echo "[3/7] recover signed prepareQueue after a lost SSH response"
fault_marker="$work_dir/response-lost"
set +e
PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="$real_ssh" PROVISION_FAULT_MARKER="$fault_marker" "$provision" deployment execute \
  --plan "$first_plan" --operation op-01 --state "$state" --signing-key "$signing_key" \
  --secret-file "$secret_reference=$secret_file" --lease-duration 5s \
  > "$work_dir/lost-response.txt" 2>&1
lost_result=$?
set -e
[[ $lost_result -ne 0 && -f "$fault_marker" ]] || { echo "prepareQueue response was not deliberately lost" >&2; exit 1; }
sleep 6
"$provision" deployment resume \
  --plan "$first_plan" --state "$state" --signing-key "$signing_key" \
  --secret-file "$secret_reference=$secret_file" --lease-duration 5s \
  > "$work_dir/execution.json"
"$provision" deployment status --plan "$first_plan" --state "$state" > "$work_dir/status.json"
assert_resume_provenance "$work_dir/status.json"

echo "[4/7] inspect exact managed Queue generation"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-after.json"
grep -Fq '"health": "healthy"' "$work_dir/bootstrap-after.json"
grep -Fq '"accepted": 1' "$work_dir/bootstrap-after.json"
grep -Fq '"acknowledged": 1' "$work_dir/bootstrap-after.json"
grep -Fq '"deliveryLimit": 3' "$work_dir/bootstrap-after.json"
grep -Fq '"retryDelay": "10s"' "$work_dir/bootstrap-after.json"
grep -Fq '"deadLetterTtl": "168h0m0s"' "$work_dir/bootstrap-after.json"

echo "[5/7] re-plan and observe exact existing generation without duplication"
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

echo "[6/7] verify secret redaction and durable journal"
"$provision" deployment status --plan "$second_plan" --state "$second_state" > "$work_dir/reexecution-status.json"
assert_secret_absent
grep -Fq '"outcome": "succeeded"' "$work_dir/status.json"
grep -Fq '"outcome": "succeeded"' "$work_dir/reexecution-status.json"

echo "[7/7] verify unrelated Host inventory is unchanged"
capture_unrelated_inventory after
for inventory in system-units packages unrelated-quadlets; do
  cmp -s "$work_dir/before-$inventory.txt" "$work_dir/after-$inventory.txt" || {
    echo "unrelated Host inventory changed: $inventory" >&2
    diff -u "$work_dir/before-$inventory.txt" "$work_dir/after-$inventory.txt" >&2 || true
    exit 1
  }
done

echo "managed Queue acceptance passed"
echo "evidence: $work_dir"
