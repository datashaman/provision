#!/usr/bin/env bash
# Live off-host ssh:// PostgreSQL backup proof (issue #94), one scenario per disposable
# Host reset. Two stages so the acceptance runner can inject a destination fault between
# them: `prepare` provisions the Database up to the backup operation; `backup` runs
# backupDatabase and verifyDatabaseRestore against the second VM and asserts the outcome.
#   direct-local: run on the Host Target as its bootstrap operator.
#   strict-ssh:   run on the controller; the Host Target is reached over pinned strict SSH.
set -euo pipefail

usage() {
  echo "usage: $0 --transport direct-local|strict-ssh --stage prepare|backup --scenario success|unreachable|host-key-mismatch --provision BINARY --signing-key FILE --secret-file FILE --backup-key FILE --work-dir PATH --provision-version VERSION --destination-host ADDRESS --destination-path PATH --destination-user USER --host-key-pin SHA256:... --confirm-disposable-host HOSTNAME [--target ADDRESS --target-user USER]" >&2
  exit 2
}

transport="" stage="" scenario="" provision="" signing_key="" secret_file="" backup_key="" work_dir=""
provision_version="" dest_host="" dest_path="" dest_user="" pin="" confirmed_host="" target_address="" target_user=""
while (($#)); do
  (($# >= 2)) || usage
  case "$1" in
    --transport) transport="$2" ;;
    --stage) stage="$2" ;;
    --scenario) scenario="$2" ;;
    --provision) provision="$2" ;;
    --signing-key) signing_key="$2" ;;
    --secret-file) secret_file="$2" ;;
    --backup-key) backup_key="$2" ;;
    --work-dir) work_dir="$2" ;;
    --provision-version) provision_version="$2" ;;
    --destination-host) dest_host="$2" ;;
    --destination-path) dest_path="$2" ;;
    --destination-user) dest_user="$2" ;;
    --host-key-pin) pin="$2" ;;
    --confirm-disposable-host) confirmed_host="$2" ;;
    --target) target_address="$2" ;;
    --target-user) target_user="$2" ;;
    *) usage ;;
  esac
  shift 2
done

[[ "$transport" == direct-local || "$transport" == strict-ssh ]] || usage
[[ "$stage" == prepare || "$stage" == backup ]] || usage
[[ "$scenario" == success || "$scenario" == unreachable || "$scenario" == host-key-mismatch ]] || usage
for file in "$provision" "$signing_key" "$secret_file" "$backup_key"; do
  [[ -n "$file" && -f "$file" && ! -L "$file" ]] || usage
done
[[ -n "$work_dir" && -n "$provision_version" && -n "$dest_host" && -n "$dest_path" && -n "$dest_user" && -n "$confirmed_host" ]] || usage
[[ "$pin" =~ ^SHA256:[A-Za-z0-9+/]{43}$ ]] || usage
[[ "$(id -u)" != 0 ]] || { echo "run as a non-root user" >&2; exit 1; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
provision="$(cd "$(dirname "$provision")" && pwd)/$(basename "$provision")"
signing_key="$(cd "$(dirname "$signing_key")" && pwd)/$(basename "$signing_key")"
secret_file="$(cd "$(dirname "$secret_file")" && pwd)/$(basename "$secret_file")"
backup_key="$(cd "$(dirname "$backup_key")" && pwd)/$(basename "$backup_key")"

if [[ "$transport" == strict-ssh ]]; then
  [[ "$target_address" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage
  actor="$(id -un)"
  operator="$target_user"
  actual_host="$(ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 "$target_user@$target_address" hostname)"
  host_args=(--address "$target_address" --user "$target_user")
else
  operator="$(id -un)"
  actor="$operator"
  actual_host="$(hostname)"
  host_args=(--local)
fi
[[ "$confirmed_host" == "$actual_host" ]] || { echo "disposable-host confirmation does not match $actual_host" >&2; exit 1; }

state="$work_dir/state.db"
config_dir="$work_dir/config"
dir="$work_dir/$scenario"
secret_reference="secret://lab/postgresql-url"
backup_reference="secret://lab/backup-ssh-key"
secrets=(--secret-file "$secret_reference=$secret_file" --secret-file "$backup_reference=$backup_key")

fail() { echo "off-host ssh PostgreSQL ($transport/$scenario): $*" >&2; exit 1; }

json_get() { # json_get FILE dotted.path
  python3 - "$1" "$2" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
for segment in sys.argv[2].split("."):
    value = value[int(segment)] if isinstance(value, list) else value[segment]
print(json.dumps(value) if not isinstance(value, str) else value)
PY
}

json_assert() { [[ "$(json_get "$1" "$2")" == "$3" ]] || fail "$1: $2 = $(json_get "$1" "$2"), expected $3"; }

operations() { # "id kind" per line, Plan order
  python3 - "$dir/plan.json" <<'PY'
import json, sys
for op in json.load(open(sys.argv[1], encoding="utf-8"))["operations"]:
    print(op["id"], op["kind"])
PY
}

execute() { # execute OPERATION [lease]
  "$provision" deployment execute --plan "$plan_id" --operation "$1" --state "$state" \
    --signing-key "$signing_key" "${secrets[@]}" --lease-duration "${2:-2m}" </dev/null
}

assert_no_key_material() {
  local body
  body="$(sed -n '2,$p' "$backup_key" | grep -v -- '-----' | head -1 | tr -d '\n')"
  [[ -n "$body" ]] || fail "backup key has no body"
  ! grep -R --binary-files=without-match -F "$body" "$work_dir" >/dev/null 2>&1 || fail "backup private key appeared in evidence"
}

if [[ "$stage" == prepare ]]; then
  [[ ! -e "$work_dir" ]] || fail "work directory already exists"
  mkdir -m 0700 "$work_dir" "$config_dir" "$dir"
  cp "$repo_root"/examples/host-database/{application,revision,root,environment}.yaml "$config_dir/"
  python3 - "$config_dir" "$dest_host" "$dest_path" "$dest_user" "$pin" "$backup_reference" "$transport" "$target_address" "$target_user" "$operator" <<'PY'
import sys
from pathlib import Path
config, host, path, user, pin, reference, transport, address, target_user, operator = sys.argv[1:]
app = Path(config, "application.yaml")
out = []
for line in app.read_text(encoding="utf-8").splitlines():
    indent = line[: len(line) - len(line.lstrip())]
    if line.strip().startswith("destination:"):
        out += [f"{indent}destination: ssh://{host}{path}", f"{indent}sshUser: {user}", f"{indent}hostKey: {pin}", f"{indent}credentialReference: {reference}"]
    elif line.strip() == "hostLoss: not-declared":
        out.append(f"{indent}hostLoss: off-host-backup-required")
    else:
        out.append(line)
app.write_text("\n".join(out) + "\n", encoding="utf-8")
env = Path(config, "environment.yaml")
out = []
for line in env.read_text(encoding="utf-8").splitlines():
    if line.strip().startswith("address:"):
        out.append("    local: true" if transport == "direct-local" else f"    address: {address}")
    elif line.strip().startswith("user:"):
        out.append(f"    user: {operator}")
    else:
        out.append(line)
env.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
  "$provision" host bootstrap check "${host_args[@]}" --environment lab --operator "$operator" >"$work_dir/initial-host.json"
  json_assert "$work_dir/initial-host.json" ready true
  "$provision" config validate --file "$config_dir/root.yaml" >"$dir/validation.json"
  "$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$dir/plan.json"
  plan_id="$(json_get "$dir/plan.json" id)"
  [[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity"
  "$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$actor" --state "$state" --expires-after 2h >"$dir/approval.json"
  json_assert "$dir/approval.json" eligible true
  echo "$plan_id" >"$work_dir/plan_id"
  while read -r id kind; do
    [[ "$kind" == backupDatabase ]] && break
    execute "$id" >"$dir/$id.json"
    json_assert "$dir/$id.json" outcome succeeded
  done < <(operations)
  echo "prepared: Database provisioned, next operation is backupDatabase"
  exit 0
fi

[[ -d "$dir" && -f "$work_dir/plan_id" ]] || fail "run --stage prepare first"
plan_id="$(cat "$work_dir/plan_id")"
backup_op="" restore_op=""
while read -r id kind; do
  [[ "$kind" == backupDatabase ]] && backup_op="$id"
  [[ "$kind" == verifyDatabaseRestore ]] && restore_op="$id"
done < <(operations)
[[ -n "$backup_op" && -n "$restore_op" ]] || fail "Plan has no backup and restore verification operations"

if [[ "$scenario" != success ]]; then
  expected="backup-destination-${scenario}"
  set +e
  execute "$backup_op" >"$dir/$backup_op.txt" 2>&1
  status=$?
  set -e
  [[ $status -ne 0 ]] || fail "backup unexpectedly succeeded against a faulted destination"
  grep -Fq "$expected" "$dir/$backup_op.txt" || fail "$backup_op did not report $expected: $(head -c 400 "$dir/$backup_op.txt")"
  "$provision" deployment status --plan "$plan_id" --state "$state" >"$dir/status.json"
  python3 - "$dir/status.json" "$backup_op" <<'PY' || fail "journal does not record $backup_op as failed"
import json, sys
events = [e for e in json.load(open(sys.argv[1], encoding="utf-8"))["events"] if e["operationId"] == sys.argv[2]]
assert events[-1]["kind"] == "outcome" and events[-1]["outcome"] == "failed", events[-1]
PY
  assert_no_key_material
  python3 - "$dir/summary.json" "$transport" "$scenario" "$expected" "$dest_host" "$pin" "$provision_version" <<'PY'
import json, sys
path, transport, scenario, category, host, pin, version = sys.argv[1:]
json.dump({"schemaVersion": "provision.dev/offhost-ssh-postgresql-observation/v1alpha1", "result": "passed", "transport": transport,
           "scenario": scenario, "failureCategory": category, "outcome": "failed", "destinationHost": host, "pinnedHostKey": pin,
           "provisionSourceVersion": version}, open(path, "w", encoding="utf-8"), indent=2, sort_keys=True)
PY
  echo "$transport/$scenario passed: $expected, outcome failed"
  exit 0
fi

capturing=0
while read -r id kind; do
  [[ "$id" == "$backup_op" ]] && capturing=1
  ((capturing)) || continue
  execute "$id" >"$dir/$id.json"
  json_assert "$dir/$id.json" outcome succeeded
done < <(operations)

b="$dir/$backup_op.json" r="$dir/$restore_op.json"
json_assert "$b" observation.backup.destinationClass off-host-ssh
json_assert "$b" observation.backup.destinationHost "$dest_host"
json_assert "$b" observation.backup.hostKeyFingerprint "$pin"
json_assert "$b" observation.backup.outsideGeneration true
json_assert "$r" observation.restore.destinationHost "$dest_host"
json_assert "$r" observation.restore.hostKeyFingerprint "$pin"
json_assert "$r" observation.restore.isolated true
json_assert "$r" observation.restore.verified true
digest="$(json_get "$b" observation.backup.artifactDigest)"
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ && "$digest" == "$(json_get "$r" observation.restore.artifactDigest)" ]] || fail "backup and restore artifact digests differ or are malformed"
assert_no_key_material
python3 - "$dir/summary.json" "$transport" "$dest_host" "$pin" "$digest" "$(json_get "$b" observation.backup.backupId)" "$provision_version" "$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$provision")" <<'PY'
import json, sys
path, transport, host, pin, digest, backup_id, version, binary = sys.argv[1:]
json.dump({"schemaVersion": "provision.dev/offhost-ssh-postgresql-observation/v1alpha1", "result": "passed", "transport": transport,
           "scenario": "success", "destinationHost": host, "pinnedHostKey": pin, "artifactDigest": digest, "backupId": backup_id,
           "provisionSourceVersion": version, "provisionBinarySha256": "sha256:" + binary}, open(path, "w", encoding="utf-8"), indent=2, sort_keys=True)
PY
echo "$transport/success passed: artifact $digest on $dest_host"
