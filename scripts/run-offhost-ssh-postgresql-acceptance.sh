#!/usr/bin/env bash
# Disposable-VM acceptance for issue #94: off-host ssh:// PostgreSQL backups with a second Incus VM
# as the destination. For each transport (direct-local, strict-ssh) and scenario (success, unreachable,
# host-key-mismatch) both VMs are restored from their clean snapshots, so one scenario cannot leak into the next.
#
# One-time destination VM setup (not scripted): an Incus VM named provision-backup-dest with a clean snapshot,
# a no-sudo `pgbackup` user whose authorized_keys holds the public half of PROVISION_BACKUP_KEY, and
# /srv/provision-backups owned by it (mode 0700). cloud-init's `lock_passwd: true` leaves `!` in /etc/shadow, which
# sshd without PAM rejects as a locked account, so run `usermod -p '*' pgbackup` before taking the snapshot.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
base="${PROVISION_ACCEPTANCE_BASE:-base.local}"
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
host_name="${PROVISION_ACCEPTANCE_HOSTNAME:-provision-acceptance}"
dest_instance="${PROVISION_BACKUP_DEST_INSTANCE:-provision-backup-dest}"
dest_user="${PROVISION_BACKUP_DEST_USER:-pgbackup}"
dest_path="${PROVISION_BACKUP_DEST_PATH:-/srv/provision-backups}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_POSTGRESQL_SECRET:-$root/work/issue-75-live/postgresql-url.secret}"
backup_key="${PROVISION_BACKUP_KEY:-$root/work/issue-94-live/backup_key}"
build_dir="$root/work/issue-94-live"
local_evidence="${PROVISION_ISSUE94_EVIDENCE:-$build_dir/evidence}"
remote="/tmp/provision-issue94"
scenarios="${PROVISION_ACCEPTANCE_SCENARIOS:-success unreachable host-key-mismatch}"
transports="${PROVISION_ACCEPTANCE_TRANSPORTS:-direct-local strict-ssh}"
ssh_opts=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #94 off-host ssh:// PostgreSQL backup acceptance\n'
printf 'Host Target: %s@%s (disposable acceptance VM)\nBackup destination: Incus VM %s on %s\n' "$target_user" "$target" "$dest_instance" "$base"
printf 'Per scenario this restores BOTH VMs from their clean snapshots, bootstraps the Host Target, injects the destination fault, and restores both again at the end.\n'

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" && -f "$secret" && -f "$backup_key" ]] || {
  printf 'missing authority, database secret, or backup key (PROVISION_BACKUP_KEY)\n' >&2
  exit 2
}
chmod 0600 "$backup_key"

restore_destination() {
  ssh "${ssh_opts[@]}" "$base" bash -s -- "$dest_instance" <<'REMOTE'
set -euo pipefail
instance="$1"
[[ "$(incus list "$instance" --format csv -c s)" == STOPPED ]] || incus stop "$instance" --force
incus snapshot restore "$instance" clean
incus start "$instance"
for _ in $(seq 1 90); do incus exec "$instance" -- true >/dev/null 2>&1 && exit 0; sleep 2; done
exit 1
REMOTE
  dest_host=""
  for _ in $(seq 1 60); do
    dest_host="$(ssh "${ssh_opts[@]}" "$base" incus list "$dest_instance" --format csv -c 4 | sed -E 's/ .*//')"
    [[ "$dest_host" =~ ^[0-9.]+$ ]] && ssh-keyscan -T 3 -t ed25519 "$dest_host" >/dev/null 2>&1 && return 0
    sleep 2
  done
  return 1
}

destination_pin() {
  ssh-keyscan -T 10 -t ed25519 "$dest_host" 2>/dev/null | ssh-keygen -lf - | awk '{print $2}'
}

cleanup() {
  local status=$?
  # PROVISION_ACCEPTANCE_KEEP_VM=1 leaves a failed VM in place for diagnosis.
  if [[ "$status" -ne 0 && -z "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]]; then
    "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
    restore_destination >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

mkdir -p "$build_dir"
GOOS="$(go env GOOS)" GOARCH="$(go env GOARCH)" go build -o "$build_dir/provision-controller" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)"
if ! git -C "$root" diff --quiet HEAD --; then
  source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
fi

reset_and_bootstrap() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" "rm -rf '$remote' && mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" "$build_dir/provision" "$build_dir/provision-host-executor" "$authority/host-authority.pub" \
    "$authority/host-authority.key" "$secret" "$backup_key" "$target_user@$target:$remote/"
  scp "${scp_opts[@]}" -r "$root/scripts" "$root/examples" "$target_user@$target:$remote/"
  # shellcheck disable=SC2029
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "chmod 0600 '$remote/host-authority.key' '$remote/$(basename "$secret")' '$remote/$(basename "$backup_key")' && sudo -n bash '$remote/scripts/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
}

inject() {
  case "$1" in
    success) ;;
    unreachable) ssh "${ssh_opts[@]}" "$base" incus stop "$dest_instance" --force ;;
    host-key-mismatch)
      ssh "${ssh_opts[@]}" "$base" incus exec "$dest_instance" -- bash -s <<'REMOTE' ;;
set -e
rm -f /etc/ssh/ssh_host_*
ssh-keygen -A >/dev/null
systemctl restart ssh
REMOTE
  esac
}

# stage_args: arguments shared by every invocation of the test script for one transport/scenario.
run_scenario() {
  local transport="$1" scenario="$2" evidence="$local_evidence/$1/$2"
  local common=(--transport "$transport" --scenario "$scenario" --destination-host "$dest_host" --destination-path "$dest_path"
    --destination-user "$dest_user" --host-key-pin "$pin" --provision-version "$source_version" --confirm-disposable-host "$host_name")
  mkdir -p "$evidence"
  restore_destination
  [[ "$(destination_pin)" == "$pin" ]] || { echo "destination host key differs from the pinned clean-snapshot key" >&2; return 1; }
  reset_and_bootstrap
  if [[ "$transport" == direct-local ]]; then
    local t=("$remote/scripts/test-offhost-ssh-postgresql-host.sh" "${common[@]}" --provision "$remote/provision"
      --signing-key "$remote/host-authority.key" --secret-file "$remote/$(basename "$secret")" --backup-key "$remote/$(basename "$backup_key")" --work-dir "$remote/work")
    # shellcheck disable=SC2029
    ssh "${ssh_opts[@]}" "$target_user@$target" "cd '$remote' && $(printf '%q ' "${t[@]}") --stage prepare"
    inject "$scenario"
    # shellcheck disable=SC2029
    ssh "${ssh_opts[@]}" "$target_user@$target" "cd '$remote' && $(printf '%q ' "${t[@]}") --stage backup"
    scp "${scp_opts[@]}" -r "$target_user@$target:$remote/work/." "$evidence/"
  else
    local t=("$root/scripts/test-offhost-ssh-postgresql-host.sh" "${common[@]}" --provision "$build_dir/provision-controller"
      --signing-key "$authority/host-authority.key" --secret-file "$secret" --backup-key "$backup_key" --work-dir "$evidence/work"
      --target "$target" --target-user "$target_user")
    rm -rf "$evidence/work"
    "${t[@]}" --stage prepare
    inject "$scenario"
    "${t[@]}" --stage backup
  fi
  if [[ "$scenario" == success ]]; then
    # Independent destination-side proof: the artifact on the second VM matches the digest in the evidence.
    local digest backup_id summary
    summary="$(find "$evidence" -name summary.json | head -1)"
    digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["artifactDigest"])' "$summary")"
    backup_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["backupId"])' "$summary")"
    ssh "${ssh_opts[@]}" "$base" incus exec "$dest_instance" -- bash -s -- "$dest_path" "$backup_id" <<'REMOTE' >"$evidence/destination-inventory.txt"
find "$1" -type f -printf '%m %u %s %p\n'
sha256sum "$1/provision-lab-data/$2.dump"
REMOTE
    awk '$0 ~ /^[0-9a-f]{64} /' "$evidence/destination-inventory.txt" >"$evidence/destination-sha256.txt"
    [[ "sha256:$(awk '{print $1}' "$evidence/destination-sha256.txt")" == "$digest" ]] || { echo "destination artifact digest does not match the evidence" >&2; return 1; }
  fi
}

[[ ! -e "$local_evidence" ]] || mv "$local_evidence" "$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$local_evidence"
restore_destination
pin="$(destination_pin)"
[[ "$pin" =~ ^SHA256: ]] || { echo "cannot read the destination host key" >&2; exit 1; }
printf 'Destination %s pinned as %s\n' "$dest_host" "$pin"
for transport in $transports; do
  for scenario in $scenarios; do
    printf '\n== %s / %s ==\n' "$transport" "$scenario"
    run_scenario "$transport" "$scenario"
  done
done

"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
restore_destination
trap - EXIT
printf '\nOff-host ssh:// PostgreSQL acceptance passed.\nevidence: %s\n' "$local_evidence"
