#!/usr/bin/env bash
# shellcheck disable=SC2029 # Remote commands intentionally expand client-side acceptance paths.
# Disposable-VM acceptance for issue #101: the direct-local Valkey Store Transition failure matrix
# (#100), the same matrix over pinned strict SSH from this controller, then a parity comparison.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
host_name="${PROVISION_ACCEPTANCE_HOSTNAME:-provision-acceptance}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_VALKEY_SECRET:-$root/work/issue-101-live/valkey-url.secret}"
build_dir="$root/work/issue-101-live"
local_evidence="${PROVISION_ISSUE101_EVIDENCE:-$build_dir/parity-evidence}"
direct_evidence="$local_evidence/direct-local"
ssh_evidence="$local_evidence/remote-ssh"
modes="${PROVISION_ACCEPTANCE_MODES:-before after undecidable}"
read -ra mode_list <<<"$modes"
remote="/tmp/provision-issue101-bootstrap"
ssh_opts=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=20 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=20 -o ConnectTimeout=15)

printf 'Provision issue #101 remote-SSH Valkey transition parity on %s@%s (disposable VM; clean snapshot restored per mode)\n' "$target_user" "$target"

cleanup() {
  local status=$?
  [[ "$status" -eq 0 || -n "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]] || "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" ]] || { echo "missing authority files; set PROVISION_ACCEPTANCE_AUTHORITY" >&2; exit 2; }
if [[ ! -f "$secret" ]]; then
  mkdir -p "$(dirname "$secret")"
  umask 077
  printf 'valkey://app:Issue101Valkey-Secret-123456789@127.0.0.1:26379\n' >"$secret"
fi
[[ ! -L "$secret" ]] || { echo "secret file must not be a symlink" >&2; exit 2; }

mkdir -p "$build_dir"
GOOS="$(go env GOOS)" GOARCH="$(go env GOARCH)" go build -o "$build_dir/provision-controller" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)"
git -C "$root" diff --quiet HEAD -- || source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
bash -n "$root/scripts/test-direct-local-valkey-transition-host.sh" "$root/scripts/run-remote-ssh-valkey-transition-matrix.sh"
[[ -n "${PROVISION_ACCEPTANCE_SKIP_GO_TEST:-}" ]] || go test ./cmd/... ./internal/... >/dev/null

[[ ! -e "$local_evidence" ]] || mv "$local_evidence" "$local_evidence.old-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$local_evidence" "$ssh_evidence"

if [[ -z "${PROVISION_ACCEPTANCE_SKIP_DIRECT:-}" ]]; then
  PROVISION_ISSUE100_EVIDENCE="$direct_evidence" PROVISION_ACCEPTANCE_MODES="$modes" "$root/scripts/run-direct-local-valkey-transition-matrix.sh"
fi

run_ssh_mode() {
  local mode="$1" attempt
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  # The restored snapshot is old, so apt's catch-up timers fire after boot and starve the 2-vCPU VM for minutes,
  # long enough to expire a 30s execution lease mid-operation. Stop them for this boot.
  ssh "${ssh_opts[@]}" "$target_user@$target" "sudo -n systemctl stop apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service"
  ssh "${ssh_opts[@]}" "$target_user@$target" "rm -rf '$remote'; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" "$root/scripts/bootstrap-host.sh" "$build_dir/provision-host-executor" "$authority/host-authority.pub" "$target_user@$target:$remote/"
  # The package install can restart VM networking and drop this session; bootstrap is idempotent, so retry.
  for attempt in 1 2 3; do
    ssh "${ssh_opts[@]}" "$target_user@$target" \
      "sudo -n bash '$remote/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'" && break
    [[ $attempt -lt 3 ]] || { echo "bootstrap failed after $attempt attempts" >&2; exit 1; }
    until ssh "${ssh_opts[@]}" "$target_user@$target" true 2>/dev/null; do sleep 5; done
  done
  "$root/scripts/test-direct-local-valkey-transition-host.sh" --provision "$build_dir/provision-controller" --signing-key "$authority/host-authority.key" --secret-file "$secret" \
    --work-dir "$ssh_evidence/$mode" --provision-version "$source_version" --target "$target" --target-user "$target_user" \
    --confirm-disposable-host "$host_name" --fault-mode "$mode"
}
for mode in "${mode_list[@]}"; do run_ssh_mode "$mode"; done

"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
# The restored VM must hold no Provision-owned Environment state.
if ssh "${ssh_opts[@]}" "$target_user@$target" "test -e /var/lib/provision/environments/lab"; then
  echo "VM is not clean after restore" >&2; exit 1
fi

if [[ -z "${PROVISION_ACCEPTANCE_SKIP_DIRECT:-}" ]]; then
  python3 "$root/scripts/valkey-transition-parity.py" "$local_evidence/parity-summary.json" "$source_version" "$direct_evidence" "$ssh_evidence" "${mode_list[@]}"
fi
trap - EXIT
printf '\nRemote-SSH Valkey transition parity passed.\nevidence: %s\n' "$local_evidence"
