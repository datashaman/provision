#!/usr/bin/env bash
# Controller-side, issue-36-only acceptance orchestration. It restores the
# named disposable VM before prepare and again after evidence capture.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: run-rabbitmq-quadlet-acceptance.sh (--preview|--run) \
  --base-target USER@HOST --instance NAME --snapshot NAME --guest-target USER@HOST \
  --environment NAME --oci-image NAME@sha256:DIGEST --probe-binary FILE \
  --host-proof FILE --qualification-validator FILE --approval-record FILE \
  --operator NAME --expected-podman-version VERSION \
  --confirm-disposable-instance NAME --evidence-dir DIRECTORY
EOF
  exit 2
}

die() {
  printf 'run-rabbitmq-quadlet-acceptance: %s\n' "$*" >&2
  exit 1
}

mode=""
base_target=""
instance=""
snapshot=""
guest_target=""
environment=""
oci_image=""
probe_binary=""
host_proof=""
qualification_validator=""
approval_record=""
operator=""
expected_podman_version=""
confirmed_instance=""
evidence_dir=""
while (($#)); do
  case "$1" in
    --preview|--run) [[ -z "$mode" ]] || usage; mode="$1"; shift ;;
    --base-target) (($# >= 2)) || usage; base_target="$2"; shift 2 ;;
    --instance) (($# >= 2)) || usage; instance="$2"; shift 2 ;;
    --snapshot) (($# >= 2)) || usage; snapshot="$2"; shift 2 ;;
    --guest-target) (($# >= 2)) || usage; guest_target="$2"; shift 2 ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --oci-image) (($# >= 2)) || usage; oci_image="$2"; shift 2 ;;
    --probe-binary) (($# >= 2)) || usage; probe_binary="$2"; shift 2 ;;
    --host-proof) (($# >= 2)) || usage; host_proof="$2"; shift 2 ;;
    --qualification-validator) (($# >= 2)) || usage; qualification_validator="$2"; shift 2 ;;
    --approval-record) (($# >= 2)) || usage; approval_record="$2"; shift 2 ;;
    --operator) (($# >= 2)) || usage; operator="$2"; shift 2 ;;
    --expected-podman-version) (($# >= 2)) || usage; expected_podman_version="$2"; shift 2 ;;
    --confirm-disposable-instance) (($# >= 2)) || usage; confirmed_instance="$2"; shift 2 ;;
    --evidence-dir) (($# >= 2)) || usage; evidence_dir="$2"; shift 2 ;;
    *) usage ;;
  esac
done

target_pattern='^([a-z_][a-z0-9_-]*@)?[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$'
[[ -n "$mode" && "$base_target" =~ $target_pattern && "$guest_target" =~ $target_pattern ]] || usage
[[ "$instance" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$ ]] || usage
[[ "$snapshot" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$ ]] || usage
[[ "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ ]] || usage
[[ "$oci_image" =~ ^[a-z0-9.-]+([:/][a-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]] || usage
[[ "$operator" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || usage
[[ "$expected_podman_version" =~ ^[A-Za-z0-9.+:~_-]+$ ]] || usage
[[ "$confirmed_instance" == "$instance" ]] || die "disposable-instance confirmation does not match $instance"
[[ -n "$evidence_dir" && "$evidence_dir" != / ]] || usage
for file in "$probe_binary" "$host_proof" "$qualification_validator" "$approval_record"; do
  [[ -f "$file" && ! -L "$file" ]] || die "required input is missing or is a symlink: $file"
done

printf 'RabbitMQ acceptance controller preview:\n'
printf '  Base target: %s\n' "$base_target"
printf '  Snapshot: %s/%s\n' "$instance" "$snapshot"
printf '  Guest target: %s\n' "$guest_target"
printf '  Approval record: %s\n' "$approval_record"
printf '  Evidence: %s\n' "$evidence_dir"
printf '  Restores: before prepare and after evidence capture\n'
if [[ "$mode" == --preview ]]; then
  printf 'No changes made.\n'
  exit 0
fi

[[ -x "$probe_binary" ]] || die "probe binary is not executable"
[[ -x "$host_proof" && -x "$qualification_validator" ]] || die "proof tools are not executable"
[[ ! -e "$evidence_dir" ]] || die "evidence directory already exists"
mkdir -p "$evidence_dir/host"

ssh_options=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10)

restore_instance() {
  ssh "${ssh_options[@]}" "$base_target" bash -s -- "$instance" "$snapshot" <<'REMOTE'
set -euo pipefail
instance="$1"
snapshot="$2"
incus info "$instance" >/dev/null
incus snapshot list "$instance" --format csv -c n | grep -Fxq "$snapshot"
status="$(incus list "$instance" --format csv -c s)"
if [[ "$status" != STOPPED ]]; then
  incus stop "$instance" --timeout 60
fi
incus snapshot restore "$instance" "$snapshot"
incus start "$instance"
for _ in $(seq 1 90); do
  incus exec "$instance" -- true >/dev/null 2>&1 && exit 0
  sleep 2
done
exit 1
REMOTE
}

wait_for_guest() {
  for _ in $(seq 1 60); do
    ssh "${ssh_options[@]}" "$guest_target" true >/dev/null 2>&1 && return 0
    sleep 2
  done
  return 1
}

observe_clean_guest() {
  local destination="$1"
  ssh "${ssh_options[@]}" "$guest_target" python3 - "$environment" <<'PY' > "$destination"
import json
import os
import pwd
import shutil
import sys

environment = sys.argv[1]
account = f"provision-{environment}"
try:
    pwd.getpwnam(account)
    account_present = True
except KeyError:
    account_present = False
result = {
    "schemaVersion": "provision.dev/rabbitmq-clean-host-observation/v1alpha1",
    "bootId": open("/proc/sys/kernel/random/boot_id", encoding="utf-8").read().strip(),
    "podmanPresent": shutil.which("podman") is not None,
    "environmentAccountPresent": account_present,
    "issueEvidencePresent": os.path.exists("/var/lib/provision/evidence/issue36"),
}
result["clean"] = not any((
    result["podmanPresent"],
    result["environmentAccountPresent"],
    result["issueEvidencePresent"],
))
print(json.dumps(result, indent=2, sort_keys=True))
PY
}

require_clean_observation() {
  python3 - "$1" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    observation = json.load(handle)
if observation.get("clean") is not True:
    raise SystemExit("restored Host is not clean")
PY
}

ssh "${ssh_options[@]}" "$base_target" incus snapshot list "$instance" --format json > "$evidence_dir/snapshot-info.json"

cleanup_needed=true
cleanup() {
  status=$?
  if [[ "$cleanup_needed" == true ]]; then
    mkdir -p "$evidence_dir/failure-host"
    scp "${ssh_options[@]}" -q -r \
      "$guest_target:/var/lib/provision/evidence/issue36/." \
      "$evidence_dir/failure-host/" >/dev/null 2>&1 || true
    if restore_instance && wait_for_guest; then
      observe_clean_guest "$evidence_dir/post-restore-clean.json" || true
    else
      printf 'run-rabbitmq-quadlet-acceptance: emergency clean-snapshot restore failed\n' >&2
    fi
  fi
  exit "$status"
}
trap cleanup EXIT

restore_instance
wait_for_guest || die "guest SSH did not become ready after the pre-prepare restore"
observe_clean_guest "$evidence_dir/pre-prepare-clean.json"
require_clean_observation "$evidence_dir/pre-prepare-clean.json"

scp "${ssh_options[@]}" -q "$host_proof" "$probe_binary" "$qualification_validator" "$approval_record" \
  "$guest_target:/tmp/"
remote_proof="/tmp/$(basename "$host_proof")"
remote_probe="/tmp/$(basename "$probe_binary")"
remote_validator="/tmp/$(basename "$qualification_validator")"
remote_approval="/tmp/$(basename "$approval_record")"

ssh "${ssh_options[@]}" "$base_target" incus exec "$instance" -- \
  bash "$remote_proof" --prepare \
  --environment "$environment" --oci-image "$oci_image" \
  --probe-binary "$remote_probe" --qualification-validator "$remote_validator" \
  --expected-podman-version "$expected_podman_version" \
  --confirm-disposable-host "$instance" --approval-record "$remote_approval" \
  --operator "$operator"

ssh "${ssh_options[@]}" "$base_target" incus restart "$instance" --timeout 60
wait_for_guest || die "guest SSH did not become ready after the qualification reboot"
scp "${ssh_options[@]}" -q "$host_proof" "$guest_target:/tmp/"
ssh "${ssh_options[@]}" "$base_target" incus exec "$instance" -- \
  bash "$remote_proof" --verify-after-reboot \
  --environment "$environment" --oci-image "$oci_image" \
  --probe-binary /usr/local/libexec/provision-rabbitmq-acceptance-probe \
  --qualification-validator /usr/local/libexec/provision-rabbitmq-qualification-validator \
  --expected-podman-version "$expected_podman_version" \
  --confirm-disposable-host "$instance" \
  --approval-record /var/lib/provision/evidence/issue36/approval-record.json \
  --operator "$operator"

scp "${ssh_options[@]}" -q -r \
  "$guest_target:/var/lib/provision/evidence/issue36/." "$evidence_dir/host/"

restore_instance
wait_for_guest || die "guest SSH did not become ready after the cleanup restore"
observe_clean_guest "$evidence_dir/post-restore-clean.json"
require_clean_observation "$evidence_dir/post-restore-clean.json"
cleanup_needed=false
trap - EXIT

python3 - "$evidence_dir" "$base_target" "$instance" "$snapshot" "$guest_target" <<'PY'
import hashlib
import json
import os
import sys

root, base, instance, snapshot, guest = sys.argv[1:]

def load(name):
    with open(os.path.join(root, name), encoding="utf-8") as handle:
        return json.load(handle)

def digest(name):
    value = hashlib.sha256()
    with open(os.path.join(root, name), "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            value.update(chunk)
    return "sha256:" + value.hexdigest()

snapshot_inventory = load("snapshot-info.json")
matches = [item for item in snapshot_inventory if item.get("name") == snapshot]
if len(matches) != 1:
    raise SystemExit("controller provenance cannot identify the exact clean snapshot")
snapshot_info = matches[0]
pre = load("pre-prepare-clean.json")
post = load("post-restore-clean.json")
qualification = load("host/support-observation.json")
if not pre.get("clean") or not post.get("clean") or qualification.get("result") != "passed":
    raise SystemExit("controller provenance cannot report passed")
result = {
    "schemaVersion": "provision.dev/rabbitmq-acceptance-controller/v1alpha1",
    "result": "passed",
    "baseTarget": base,
    "guestTarget": guest,
    "instance": instance,
    "snapshot": {
        "name": snapshot,
        "createdAt": snapshot_info.get("created_at"),
        "stateful": snapshot_info.get("stateful"),
        "rawObservationDigest": digest("snapshot-info.json"),
    },
    "restores": ["before-prepare", "after-evidence-capture"],
    "prePrepareCleanObservation": pre,
    "postRestoreCleanObservation": post,
    "qualification": {
        "schemaVersion": qualification["schemaVersion"],
        "observationDigest": digest("host/support-observation.json"),
        "result": qualification["result"],
    },
}
with open(os.path.join(root, "controller-provenance.json"), "w", encoding="utf-8") as handle:
    json.dump(result, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY

printf 'RabbitMQ acceptance controller passed\n'
printf 'evidence: %s\n' "$evidence_dir"
