#!/usr/bin/env bash
# Destructive acceptance for the first managed Valkey generation (issue #97).
# Without --target it runs on the disposable Host Target as its bootstrap
# operator (direct-local Host Handler). With --target/--target-user it runs on
# the controller and reaches the Host Target only through strict SSH. Both
# transports run identical Plans, approvals, interruption points and assertions.
set -euo pipefail

usage() {
  echo "usage: $0 --provision BINARY --signing-key FILE --secret-file FILE --work-dir EMPTY_PATH --provision-version VERSION --confirm-disposable-host HOSTNAME [--target ADDRESS --target-user USER] [--fault-mode none|before|after|undecidable]" >&2
  exit 2
}

provision="" signing_key="" secret_file="" work_dir="" provision_version="" confirmed_host="" fault_mode=none target="" target_user=""
while (($#)); do
  case "$1" in
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --provision-version) (($# >= 2)) || usage; provision_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --fault-mode) (($# >= 2)) || usage; fault_mode="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$provision" && -x "$provision" && -f "$provision" && ! -L "$provision" ]] || usage
[[ -n "$signing_key" && -f "$signing_key" && ! -L "$signing_key" ]] || usage
[[ -n "$secret_file" && -f "$secret_file" && ! -L "$secret_file" ]] || usage
[[ -n "$work_dir" && ! -e "$work_dir" && -n "$provision_version" && -n "$confirmed_host" ]] || usage
[[ "$fault_mode" =~ ^(none|before|after|undecidable)$ ]] || usage
[[ "$(id -u)" != 0 ]] || { echo "run as a non-root user" >&2; exit 1; }
command -v python3 >/dev/null

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
if [[ -n "$target" ]]; then
  transport=ssh
  [[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage
  operator="$target_user"
  actor="$(id -un)"
  fault_bin="$repo_root/scripts/remote-ssh-fault-bin"
  real_ssh="$(command -v ssh)"
  ssh_options=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
  host_run() { "$real_ssh" "${ssh_options[@]}" "$target_user@$target" "$@"; }
  host_sh() { host_run "$1"; }
  host_check() { "$provision" host bootstrap check --address "$target" --user "$target_user" --environment lab --operator "$operator"; }
else
  transport=direct-local
  operator="$(id -un)"
  actor="$operator"
  fault_bin="$repo_root/scripts/direct-local-fault-bin"
  host_run() { "$@"; }
  host_sh() { bash -c "$1"; }
  host_check() { "$provision" host bootstrap check --local --environment lab --operator "$operator"; }
fi
[[ "$confirmed_host" == "$(host_run hostname)" ]] || { echo "disposable-host confirmation does not match $(host_run hostname)" >&2; exit 1; }
host_run sudo -n true
[[ -x "$fault_bin/$([[ $transport == ssh ]] && echo ssh || echo sudo)" ]] || { echo "fault-injection helper is not executable" >&2; exit 1; }

mkdir -m 0700 "$work_dir"
work_dir="$(cd "$work_dir" && pwd)"
abs() { echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }
provision="$(abs "$provision")"; signing_key="$(abs "$signing_key")"; secret_file="$(abs "$secret_file")"
state="$work_dir/state.db"
secret_reference="secret://lab/valkey-url"
config_dir="$work_dir/config"
mkdir -m 0700 "$config_dir"

fail() { echo "Valkey acceptance ($transport/$fault_mode): $*" >&2; exit 1; }

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
python3 - "$repo_root/examples/host-key-value/environment.yaml" "$config_dir/environment.yaml" "$operator" "$target" <<'PY'
import sys
from pathlib import Path
src, dst, operator, target = sys.argv[1:]
out = []
for line in Path(src).read_text(encoding="utf-8").splitlines():
    s = line.strip()
    if s.startswith("address:"):
        out.append(f"    address: {target}" if target else "    local: true")
    elif s.startswith("user:"):
        out.append(f"    user: {operator}")
    else:
        out.append(line)
Path(dst).write_text("\n".join(out) + "\n", encoding="utf-8")
PY

host_check >"$work_dir/initial-host.json"
json_assert "$work_dir/initial-host.json" ready true
json_assert "$work_dir/initial-host.json" keyValue.capabilities.appendFsync '"everysec"'
json_assert "$work_dir/initial-host.json" keyValue.capabilities.port 26379

echo "[1/5] validate, preview, approve"
"$provision" config validate --file "$config_dir/root.yaml" >"$work_dir/validation.json"
"$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$work_dir/plan.json"
json_assert "$work_dir/plan.json" operations.0.kind '"prepareKeyValue"'
plan_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$work_dir/plan.json")"
[[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity"
"$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$actor" --state "$state" --expires-after 2h >"$work_dir/approval.json"
json_assert "$work_dir/approval.json" eligible true
generation="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["keyValue"]["generationId"])' "$work_dir/plan.json")"

journal_assert() {
  python3 - "$work_dir/$1-status.json" "$2" "$3" <<'PY'
import json, sys
path, kind, outcome = sys.argv[1:]
events = [e for e in json.load(open(path))["events"] if e["operationId"] == "op-01"]
last = events[-1]
if last["kind"] != kind or last.get("outcome", "-") != outcome:
    raise SystemExit(f"{path}: latest op-01 is {last['kind']}/{last.get('outcome', '-')}, expected {kind}/{outcome}")
if kind == "intent" and not (last.get("attemptId") and last.get("fencingToken")):
    raise SystemExit(f"{path}: paused op-01 lacks attempt/fencing evidence")
PY
}
write_status() { "$provision" deployment status --plan "$plan_id" --state "$state" >"$work_dir/$1-status.json"; }

execute_args=(--plan "$plan_id" --operation op-01 --state "$state" --signing-key "$signing_key" --secret-file "$secret_reference=$secret_file")
resume_args=(--plan "$plan_id" --state "$state" --signing-key "$signing_key" --secret-file "$secret_reference=$secret_file")
echo "[2/5] execute prepareKeyValue (fault mode: $fault_mode)"
if [[ "$fault_mode" == none ]]; then
  "$provision" deployment execute "${execute_args[@]}" --lease-duration 2m >"$work_dir/op-01.json"
else
  mode="$fault_mode"; [[ "$mode" == undecidable ]] && mode=after
  marker="$work_dir/fault-marker"
  lease=5s delay=6
  [[ "$mode" == after ]] && lease=30s delay=31
  set +e
  PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="${real_ssh:-}" PROVISION_FAULT_MODE="$mode" PROVISION_FAULT_MARKER="$marker" \
    "$provision" deployment execute "${execute_args[@]}" --lease-duration "$lease" >"$work_dir/op-01-interrupted.txt" 2>&1
  result=$?
  set -e
  [[ $result -ne 0 && -f "$marker" ]] || fail "prepareKeyValue did not inject $mode interruption"
  write_status interrupted
  journal_assert interrupted intent -
  sleep "$delay"
  if [[ "$fault_mode" == undecidable ]]; then
    # Make the generation record unreadable so resume cannot tell whether the
    # completed mutation landed; it must not replay and must record uncertainty.
    record="/var/lib/provision/environments/lab/services/valkey/generations/$generation/generation.json"
    host_run sudo -n mv "$record" "$record.orig"
    host_run sudo -n mkdir "$record"
    set +e
    "$provision" deployment resume "${resume_args[@]}" --lease-duration 2m >"$work_dir/op-01-undecidable.txt" 2>&1
    result=$?
    set -e
    host_run sudo -n rmdir "$record"
    host_run sudo -n mv "$record.orig" "$record"
    [[ $result -ne 0 ]] || fail "prepareKeyValue resumed despite an undecidable observation"
    grep -Fq "mutation was not replayed" "$work_dir/op-01-undecidable.txt" || fail "undecidable resume replayed the mutation"
    grep -Fq "outcome recorded as uncertain" "$work_dir/op-01-undecidable.txt" || fail "undecidable resume did not record uncertainty"
    write_status undecidable
    journal_assert undecidable outcome uncertain
  fi
  if [[ "$mode" == after ]]; then
    replay_marker="$work_dir/unexpected-replay"
    PATH="$fault_bin:$PATH" PROVISION_REAL_SSH="${real_ssh:-}" PROVISION_FAULT_MODE=after PROVISION_FAULT_MARKER="$replay_marker" \
      "$provision" deployment resume "${resume_args[@]}" --lease-duration 2m >"$work_dir/op-01.json"
    [[ ! -e "$replay_marker" ]] || fail "resume replayed a completed prepareKeyValue mutation"
  else
    "$provision" deployment resume "${resume_args[@]}" --lease-duration 2m >"$work_dir/op-01.json"
  fi
fi
json_assert "$work_dir/op-01.json" outcome '"succeeded"'
write_status final

echo "[3/5] inspection reports the exact observed generation"
host_check >"$work_dir/final-host.json"
json_assert "$work_dir/final-host.json" keyValue.active.health '"healthy"'
json_assert "$work_dir/final-host.json" keyValue.active.connectivity true
json_assert "$work_dir/final-host.json" keyValue.active.valkeyVersion '"9.1.2"'
json_assert "$work_dir/final-host.json" keyValue.active.appendOnly '"yes"'
json_assert "$work_dir/final-host.json" keyValue.active.appendFsync '"everysec"'
json_assert "$work_dir/final-host.json" keyValue.active.listenAddress '"127.0.0.1"'
json_assert "$work_dir/final-host.json" keyValue.active.port 26379

echo "[4/5] deterministic keys are readable from the active generation"
uid="$(host_run id -u provision-lab)"
mapfile -t keys < <(python3 -c 'import json,sys; print("\n".join(json.load(open(sys.argv[1]))["keyValue"]["binding"]["keys"]))' "$work_dir/plan.json")
accounted=0
for key in "${keys[@]}"; do
  value="$(host_sh "cd / && sudo -n runuser -u provision-lab -- env HOME=/var/lib/provision/runtime/lab XDG_RUNTIME_DIR=/run/user/$uid podman exec provision-lab-valkey /tmp/provision-cli.sh GET '$key'" | tr -d '\r')"
  [[ "$value" == "provision-deterministic-v1:$key" ]] || fail "deterministic key $key = $value"
  accounted=$((accounted + 1))
done

echo "[5/5] no resolved credential appears in plans, journals, status, or evidence"
password="$(sed -E 's#^valkey://[^:]+:([^@]+)@.*#\1#' "$secret_file" | tr -d '\n')"
[[ -n "$password" ]]
if grep -R --binary-files=without-match -F "$password" "$work_dir" >/dev/null 2>&1; then fail "resolved Valkey secret appeared in evidence"; fi
if host_run sudo -n grep -R --binary-files=without-match -F "$password" /etc/containers/systemd /var/lib/provision/environments/lab/services/valkey/credential-entrypoint /var/lib/provision/environments/lab/services/valkey/generations/"$generation"/generation.json >/dev/null 2>&1; then
  fail "resolved Valkey secret appeared in a Quadlet, entrypoint, or generation record"
fi
python3 - "$work_dir" "$transport" "$fault_mode" "$provision_version" "$plan_id" "$accounted" "$(host_run uname -sr)" <<'PY'
import hashlib, json, sys
from pathlib import Path
work, transport, mode, version, plan, accounted, kernel = sys.argv[1:]
host = json.loads((Path(work) / "final-host.json").read_text())
active = host["keyValue"]["active"]
summary = {
    "schemaVersion": "provision.dev/valkey-acceptance/v1alpha1",
    "transport": transport, "faultMode": mode, "provisionVersion": version, "plan": plan, "kernel": kernel,
    "executorDigest": host.get("executorDigest"), "systemdVersion": host.get("systemdVersion"),
    "valkey": {"version": active["valkeyVersion"], "imageManifest": active["imageManifest"], "appendOnly": active["appendOnly"], "appendFsync": active["appendFsync"], "listener": f'{active["listenAddress"]}:{active["port"]}', "podman": host["keyValue"]["capabilities"]["podmanVersion"]},
    "accounting": {"deterministicKeysReadBack": int(accounted), "outcome": "succeeded", "resolvedCredentialInEvidence": False},
    "passed": True,
}
Path(work, "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
PY
echo "Valkey acceptance ($transport/$fault_mode) passed"
