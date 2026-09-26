#!/usr/bin/env bash
# Black-box acceptance for the first Schedule -> Task -> Queue -> Worker path.
# The target must be an explicitly disposable, freshly bootstrapped Host Target.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --work-dir DIRECTORY --target HOST --target-user USER" >&2
  exit 2
}

provision=""
signing_key=""
config=""
secret_file=""
work_dir=""
target=""
target_user=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --config) (($# >= 2)) || usage; config="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
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
secret_reference="secret://lab/rabbitmq-url"

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

echo "[1/6] inspect clean asynchronous Host capability"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-before.json"

echo "[2/6] preview and approve complete asynchronous Plan"
"$provision" config validate --file "$config" > "$work_dir/configuration.json"
"$provision" plan preview --file "$config" --state "$state" > "$work_dir/plan.json"
plan="$(plan_id "$work_dir/plan.json")"
"$provision" plan approve --file "$config" --plan "$plan" --actor "$(id -un)" --state "$state" \
  > "$work_dir/approval.json"

echo "[3/6] prepare Queue and install immutable Task and Worker generations"
for operation in op-01 op-02 op-03 op-04 op-04-verify op-05 op-06 op-07 op-10 op-11; do
  args=(deployment execute --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m)
  [[ "$operation" != op-01 ]] || args+=(--secret-file "$secret_reference=$secret_file")
  "$provision" "${args[@]}" > "$work_dir/$operation.json"
done

echo "[4/6] install pinned runtime and activate one stable Schedule timer"
for operation in op-12 op-13; do
  "$provision" deployment execute \
    --plan "$plan" --operation "$operation" --state "$state" --signing-key "$signing_key" --lease-duration 2m \
    > "$work_dir/$operation.json"
done

echo "[5/6] observe one recorded occurrence, confirmed message, and Worker acknowledgement"
"$provision" deployment execute \
  --plan "$plan" --operation op-14 --state "$state" --signing-key "$signing_key" --lease-duration 2m \
  > "$work_dir/op-14.json"
"$provision" deployment status --plan "$plan" --state "$state" > "$work_dir/deployment-status.json"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-after.json"

python3 - "$work_dir/op-14.json" "$work_dir/bootstrap-after.json" <<'PY'
import json
import sys

operation = json.load(open(sys.argv[1], encoding="utf-8"))
observed = operation["observation"]
if operation["outcome"] != "succeeded" or observed["status"] != "verified":
    raise SystemExit("Schedule verification did not succeed")
if not observed.get("messageId") or not observed.get("acknowledged"):
    raise SystemExit("confirmed message was not joined to Worker acknowledgement")
invocation = observed["taskInvocation"]
occurrence = observed["occurrence"]
if invocation["id"] != occurrence["taskInvocationId"] or invocation["trigger"] != "schedule":
    raise SystemExit("occurrence and Task Invocation identities do not match")
if invocation["outcome"] != "succeeded" or len(invocation["attempts"]) != 1:
    raise SystemExit("Task Invocation attempt history is incomplete")

inspection = json.load(open(sys.argv[2], encoding="utf-8"))
status = inspection.get("async", {}).get("deployment", {})
worker = status.get("activeWorker")
if worker is None:
    raise SystemExit(f"post-run Host inspection omitted activeWorker; findings={inspection.get('findings', [])!r}; asyncFindings={inspection.get('async', {}).get('findings', [])!r}")
if worker["gate"] != "open" or not worker["queueConnected"]:
    raise SystemExit("active Worker is not open and Queue-connected")
if status["activeTask"]["id"] != invocation["taskGenerationId"]:
    raise SystemExit("active Task does not match the invocation")
if not status["schedule"]["active"] or status["schedule"]["taskGenerationId"] != status["activeTask"]["id"]:
    raise SystemExit("stable Schedule does not target the active Task generation")
if len(status.get("occurrences", [])) != 1 or len(status.get("taskInvocations", [])) != 1:
    raise SystemExit("structured status does not distinguish one occurrence and Task Invocation")
PY

echo "[6/6] verify durable journal and secret redaction"
grep -Fq '"outcome": "succeeded"' "$work_dir/deployment-status.json"
assert_secret_absent

echo "first scheduled message acceptance passed"
echo "evidence: $work_dir"
