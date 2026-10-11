#!/usr/bin/env bash
# Disposable-VM acceptance for issue #97: first managed Valkey generation.
set -euo pipefail
# shellcheck disable=SC2029 # Remote commands intentionally expand client-side acceptance paths.

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_VALKEY_SECRET:-$root/work/issue-97-live/valkey-url.secret}"
remote="/tmp/provision-issue97-direct-local"
remote_evidence="/tmp/provision-issue97-evidence"
local_evidence="${PROVISION_ISSUE97_EVIDENCE:-$root/work/issue-97-live/evidence}"
ssh_opts=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #97 direct-local Valkey acceptance on %s@%s (disposable VM; restores the clean snapshot before each mode and after success)\n' "$target_user" "$target"

cleanup_needed=false
cleanup() {
  local status=$?
  if [[ "$cleanup_needed" == true && "$status" -ne 0 ]]; then
    mkdir -p "$local_evidence.failed"
    scp "${scp_opts[@]}" -r "$target_user@$target:$remote_evidence" "$local_evidence.failed/remote" >/dev/null 2>&1 || true
    [[ -n "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]] || "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" ]] || { echo "missing authority files; set PROVISION_ACCEPTANCE_AUTHORITY" >&2; exit 2; }
if [[ ! -f "$secret" ]]; then
  mkdir -p "$(dirname "$secret")"
  umask 077
  printf 'valkey://app:Issue97Valkey-Secret-123456789@127.0.0.1:26379\n' >"$secret"
fi

build_dir="$root/work/issue-97-live"
mkdir -p "$build_dir"
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)"
git -C "$root" diff --quiet HEAD -- || source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"

bash -n "$root/scripts/test-valkey-host.sh" "$root/scripts/test-direct-local-valkey-http-host.sh" "$root/scripts/run-direct-local-valkey-acceptance.sh"
go test ./cmd/... ./internal/... >/dev/null

[[ ! -e "$local_evidence" ]] || mv "$local_evidence" "$local_evidence.old-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$local_evidence"

# The "http" mode runs the Key-Value-bound HTTP workload scenario instead.
fixture_bundle="$build_dir/valkey-http-fixture-linux-amd64.tar.gz"
build_http_fixture() {
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -buildid=" -o "$build_dir/valkey-http-fixture" ./scripts/valkey-http-fixture
  python3 - "$build_dir/valkey-http-fixture" "$fixture_bundle" <<'PY'
import gzip, io, sys, tarfile
data = open(sys.argv[1], "rb").read()
info = tarfile.TarInfo("valkey-http-fixture")
info.size, info.mode, info.uid, info.gid, info.uname, info.gname, info.mtime = len(data), 0o555, 0, 0, "root", "root", 0
buf = io.BytesIO()
with gzip.GzipFile(filename="", mode="wb", fileobj=buf, mtime=0) as gz:
    with tarfile.open(mode="w", fileobj=gz, format=tarfile.PAX_FORMAT) as tar:
        tar.addfile(info, io.BytesIO(data))
open(sys.argv[2], "wb").write(buf.getvalue())
PY
}

run_mode() {
  local mode="$1"
  cleanup_needed=true
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  ssh "${ssh_opts[@]}" "$target_user@$target" "rm -rf '$remote' '$remote_evidence'; mkdir -p '$remote' '$remote_evidence' && chmod 0700 '$remote'"
  [[ "$mode" != http ]] || { build_http_fixture; scp "${scp_opts[@]}" "$fixture_bundle" "$target_user@$target:$remote/"; }
  scp "${scp_opts[@]}" "$build_dir/provision" "$build_dir/provision-host-executor" "$authority/host-authority.pub" "$authority/host-authority.key" "$secret" "$target_user@$target:$remote/"
  scp "${scp_opts[@]}" -r "$root/scripts" "$root/examples" "$target_user@$target:$remote/"
  ssh "${ssh_opts[@]}" "$target_user@$target" "chmod 0600 '$remote/host-authority.key' '$remote/$(basename "$secret")'"
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "sudo -n bash '$remote/scripts/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
  if [[ "$mode" == http ]]; then
    ssh "${ssh_opts[@]}" "$target_user@$target" \
      "cd '$remote' && ./scripts/test-direct-local-valkey-http-host.sh --provision ./provision --signing-key ./host-authority.key --secret-file ./$(basename "$secret") --artifact-bundle ./$(basename "$fixture_bundle") --work-dir '$remote_evidence/$mode' --provision-version '$source_version' --confirm-disposable-host provision-acceptance"
  else
    ssh "${ssh_opts[@]}" "$target_user@$target" \
      "cd '$remote' && ./scripts/test-valkey-host.sh --provision ./provision --signing-key ./host-authority.key --secret-file ./$(basename "$secret") --work-dir '$remote_evidence/$mode' --provision-version '$source_version' --confirm-disposable-host provision-acceptance --fault-mode '$mode'"
  fi
  scp "${scp_opts[@]}" -r "$target_user@$target:$remote_evidence/$mode" "$local_evidence/$mode"
}

for mode in ${PROVISION_ACCEPTANCE_MODES:-none before after undecidable http}; do
  run_mode "$mode"
done
"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
cleanup_needed=false
trap - EXIT
printf '\nDirect-local Valkey acceptance passed.\nevidence: %s\n' "$local_evidence"
