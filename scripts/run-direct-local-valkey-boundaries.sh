#!/usr/bin/env bash
# shellcheck disable=SC2029 # Remote commands intentionally expand client-side acceptance paths.
# Disposable-VM acceptance for issue #102: direct-local candidate-failure and forward-only boundaries.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_VALKEY_SECRET:-$root/work/issue-102-live/valkey-url.secret}"
remote="/tmp/provision-issue102-direct-local"
remote_evidence="/tmp/provision-issue102-evidence"
local_evidence="${PROVISION_ISSUE102_EVIDENCE:-$root/work/issue-102-live/evidence}"
ssh_opts=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=20 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=20 -o ConnectTimeout=15)

printf 'Provision issue #102 direct-local Valkey acceptance on %s@%s (disposable VM; restores the clean snapshot before each scenario and after success)\n' "$target_user" "$target"

# shellcheck source=lib/valkey-vm.sh
source "$root/scripts/lib/valkey-vm.sh"

trap cleanup EXIT

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" ]] || { echo "missing authority files; set PROVISION_ACCEPTANCE_AUTHORITY" >&2; exit 2; }
if [[ ! -f "$secret" ]]; then
  mkdir -p "$(dirname "$secret")"
  umask 077
  printf 'valkey://app:Issue102Valkey-Secret-123456789@127.0.0.1:26379\n' >"$secret"
fi

build_dir="$root/work/issue-102-live"
mkdir -p "$build_dir"
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)"
git -C "$root" diff --quiet HEAD -- || source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"

bash -n "$root/scripts/test-direct-local-valkey-boundaries.sh" "$root/scripts/run-direct-local-valkey-transition-matrix.sh"
go test ./cmd/... ./internal/... >/dev/null

[[ ! -e "$local_evidence" ]] || mv "$local_evidence" "$local_evidence.old-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$local_evidence"

run_mode() {
  local mode="$1"
  vm_prepare
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "cd '$remote' && ./scripts/test-direct-local-valkey-boundaries.sh --provision ./provision --signing-key ./host-authority.key --secret-file ./$(basename "$secret") --work-dir '$remote_evidence/$mode' --provision-version '$source_version' --confirm-disposable-host provision-acceptance --scenario '$mode'"
  scp "${scp_opts[@]}" -r "$target_user@$target:$remote_evidence/$mode" "$local_evidence/$mode"
}

for mode in ${PROVISION_ACCEPTANCE_SCENARIOS:-candidate-failure forward-only}; do
  run_mode "$mode"
done
"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
# The restored VM must hold no Provision-owned Environment state.
if ssh "${ssh_opts[@]}" "$target_user@$target" "test -e /var/lib/provision/environments/lab"; then
  echo "VM is not clean after restore" >&2; exit 1
fi
printf '{"restoredAfterEveryScenario":true,"environmentStateAbsentAfterFinalRestore":true,"scenarios":"%s"}\n' "${PROVISION_ACCEPTANCE_SCENARIOS:-candidate-failure forward-only}" >"$local_evidence/vm-restore.json"
cleanup_needed=false
trap - EXIT
printf '\nValkey boundary proofs passed.\nevidence: %s\n' "$local_evidence"
