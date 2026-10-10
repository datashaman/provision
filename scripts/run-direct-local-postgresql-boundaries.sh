#!/usr/bin/env bash
# End-to-end disposable-VM proof of the #67 candidate-failure and forward-only boundaries.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_POSTGRESQL_SECRET:-$root/work/issue-75-live/postgresql-url.secret}"
remote="/tmp/provision-boundaries-direct-local"
remote_evidence="/tmp/provision-boundaries-evidence"
local_evidence="${PROVISION_BOUNDARIES_EVIDENCE:-$root/work/postgresql-boundaries-live/evidence}"
ssh_opts=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision the #67 candidate-failure and forward-only boundaries direct-local PostgreSQL acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This restores the clean VM snapshot, bootstraps the Host Target, proves a failed candidate leaves the active generation untouched and that post-cutover writes exist only in the new generation, copies evidence back, and restores the VM snapshot after success.\n'

cleanup_needed=false
preserve_remote_evidence_on_failure() {
  local destination
  destination="$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$destination"
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side evidence path.
  if ssh "${ssh_opts[@]}" "$target_user@$target" "test -e '$remote_evidence'" >/dev/null 2>&1; then
    scp "${scp_opts[@]}" -r "$target_user@$target:$remote_evidence" "$destination/remote" >/dev/null 2>&1 || true
    printf 'Preserved remote failure evidence: %s/remote\n' "$destination" >&2
  fi
}

cleanup() {
  local status=$?
  if [[ "$cleanup_needed" == true ]]; then
    if [[ "$status" -ne 0 ]]; then
      preserve_remote_evidence_on_failure
    fi
    # PROVISION_ACCEPTANCE_KEEP_VM=1 leaves a failed VM in place for diagnosis.
    if [[ "$status" -eq 0 || -z "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]]; then
      "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
    fi
  fi
  exit "$status"
}
trap cleanup EXIT

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" ]] || {
  printf 'missing authority files; set PROVISION_ACCEPTANCE_AUTHORITY\n' >&2
  exit 2
}
if [[ ! -f "$secret" ]]; then
  mkdir -p "$(dirname "$secret")"
  umask 077
  printf 'postgresql://app:Issue75PostgreSQL_Secret_123456789@127.0.0.1:25432/app\n' >"$secret"
fi
[[ -f "$secret" && ! -L "$secret" ]] || {
  printf 'missing PostgreSQL secret file; set PROVISION_ACCEPTANCE_POSTGRESQL_SECRET\n' >&2
  exit 2
}

build_dir="$root/work/postgresql-boundaries-live"
mkdir -p "$build_dir"
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
# The direct-local acceptance wrapper builds this fixture; run it once first.
fixture_bundle="$root/work/issue-75-live/provision-example-database-http-linux-amd64.tar.gz"
[[ -f "$fixture_bundle" ]] || { printf 'missing fixture bundle %s\n' "$fixture_bundle" >&2; exit 2; }
source_version="$(git -C "$root" rev-parse HEAD)"
if ! git -C "$root" diff --quiet HEAD --; then
  source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
fi

reset_and_bootstrap() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote' ]; then mv '$remote' '$remote.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" \
    "$build_dir/provision" \
    "$build_dir/provision-host-executor" \
    "$fixture_bundle" \
    "$authority/host-authority.pub" \
    "$authority/host-authority.key" \
    "$secret" \
    "$target_user@$target:$remote/"
  scp "${scp_opts[@]}" -r \
    "$root/scripts" \
    "$root/examples" \
    "$target_user@$target:$remote/"
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "chmod 0600 '$remote/host-authority.key' '$remote/$(basename "$secret")'"

  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "sudo -n bash '$remote/scripts/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
}

run_scenario() {
  local scenario="$1"
  local destination="$local_evidence/$scenario"
  local remote_destination="$remote_evidence/$scenario"

  cleanup_needed=true
  reset_and_bootstrap
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths and source identity.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "mkdir -p '$remote_evidence'; cd '$remote' && ./scripts/test-direct-local-postgresql-boundaries.sh --provision ./provision --signing-key ./host-authority.key --secret-file ./$(basename "$secret") --artifact-bundle ./$(basename "$fixture_bundle") --work-dir '$remote_destination' --provision-version '$source_version' --confirm-disposable-host provision-acceptance --scenario '$scenario'"
  mkdir -p "$(dirname "$destination")"
  scp "${scp_opts[@]}" -r "$target_user@$target:$remote_destination" "$destination"
}

if [[ -e "$local_evidence" ]]; then
  mv "$local_evidence" "$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "$local_evidence"

for scenario in ${PROVISION_ACCEPTANCE_SCENARIOS:-candidate-failure forward-only}; do
  run_scenario "$scenario"
done
"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
cleanup_needed=false
trap - EXIT

printf '\nPostgreSQL boundary proofs passed.\n'
printf 'evidence: %s\n' "$local_evidence"
