#!/usr/bin/env bash
# Disposable-VM strict-SSH parity acceptance for the first managed Valkey
# generation (issue #97): the direct-local matrix, then the same matrix over
# strict SSH from clean snapshots, then a parity comparison.
set -euo pipefail
# shellcheck disable=SC2029 # Remote commands intentionally expand client-side acceptance paths.

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
host_name="${PROVISION_ACCEPTANCE_HOSTNAME:-provision-acceptance}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_VALKEY_SECRET:-$root/work/issue-97-live/valkey-url.secret}"
build_dir="$root/work/issue-97-live"
local_evidence="${PROVISION_ISSUE97_PARITY_EVIDENCE:-$root/work/issue-97-live/parity-evidence}"
direct_evidence="$local_evidence/direct-local"
ssh_evidence="$local_evidence/remote-ssh"
modes="${PROVISION_ACCEPTANCE_MODES:-none before after undecidable}"
read -ra mode_list <<<"$modes"
remote="/tmp/provision-issue97-bootstrap"
ssh_opts=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #97 remote-SSH Valkey parity acceptance on %s@%s (disposable VM; clean snapshot restored per mode)\n' "$target_user" "$target"

cleanup() {
  local status=$?
  [[ "$status" -eq 0 || -n "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]] || "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" && -f "$secret" && ! -L "$secret" ]] || { echo "missing authority or secret files" >&2; exit 2; }
mkdir -p "$build_dir"
GOOS="$(go env GOOS)" GOARCH="$(go env GOARCH)" go build -o "$build_dir/provision-controller" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)"
git -C "$root" diff --quiet HEAD -- || source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
bash -n "$root/scripts/test-valkey-host.sh" "$root/scripts/run-remote-ssh-valkey-acceptance.sh"
shellcheck "$root/scripts/test-valkey-host.sh" "$root/scripts/run-remote-ssh-valkey-acceptance.sh" "$root/scripts/run-direct-local-valkey-acceptance.sh" 2>/dev/null || true

[[ ! -e "$local_evidence" ]] || mv "$local_evidence" "$local_evidence.old-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$local_evidence"

if [[ -z "${PROVISION_ACCEPTANCE_SKIP_DIRECT:-}" ]]; then
  PROVISION_ISSUE97_EVIDENCE="$direct_evidence" PROVISION_ACCEPTANCE_MODES="$modes" "$root/scripts/run-direct-local-valkey-acceptance.sh"
fi

run_ssh_mode() {
  local mode="$1"
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  ssh "${ssh_opts[@]}" "$target_user@$target" "rm -rf '$remote'; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" "$root/scripts/bootstrap-host.sh" "$build_dir/provision-host-executor" "$authority/host-authority.pub" "$target_user@$target:$remote/"
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "sudo -n bash '$remote/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
  mkdir -p "$ssh_evidence"
  "$root/scripts/test-valkey-host.sh" --provision "$build_dir/provision-controller" --signing-key "$authority/host-authority.key" --secret-file "$secret" \
    --work-dir "$ssh_evidence/$mode" --provision-version "$source_version" --target "$target" --target-user "$target_user" \
    --confirm-disposable-host "$host_name" --fault-mode "$mode"
}
for mode in "${mode_list[@]}"; do run_ssh_mode "$mode"; done

if [[ -z "${PROVISION_ACCEPTANCE_SKIP_DIRECT:-}" ]]; then
  python3 - "$local_evidence/parity-summary.json" "$source_version" "$direct_evidence" "$ssh_evidence" "${mode_list[@]}" <<'PY'
import json, sys
from pathlib import Path
dest, version, droot, sroot = Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]), Path(sys.argv[4])
rows = []
for mode in sys.argv[5:]:
    d = json.loads((droot / mode / "summary.json").read_text())
    r = json.loads((sroot / mode / "summary.json").read_text())
    checks = {
        "transportsDiffer": d["transport"] == "direct-local" and r["transport"] == "ssh",
        "hostPlatformMatches": d["kernel"] == r["kernel"] and d["systemdVersion"] == r["systemdVersion"],
        "executorDigestMatches": d["executorDigest"] == r["executorDigest"],
        "valkeyMatches": d["valkey"] == r["valkey"],
        "accountingMatches": d["accounting"] == r["accounting"],
        "bothPassed": d["passed"] and r["passed"],
    }
    if not all(checks.values()):
        raise SystemExit(f"{mode} parity check failed: {checks!r}")
    rows.append({"faultMode": mode, "checks": checks, "valkey": r["valkey"], "accounting": r["accounting"], "executorDigest": r["executorDigest"], "kernel": r["kernel"]})
dest.write_text(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-valkey-parity/v1alpha1", "sourceVersion": version, "comparisons": rows,
    "supportQualification": {
        "qualified": ["exactly the combination in each comparison: Provision, executor, Valkey, Podman, systemd and single-node topology; first authoritative Key-Value Store generation over direct-local and strict SSH with identical accounting"],
        "excluded": ["Store Transition, replication, write fence, rollback guarantee", "backup, restore, host-loss recovery", "derived or ephemeral roles", "multi-node Valkey", "ElastiCache", "external Valkey", "untested host or runtime versions"],
    },
}, indent=2, sort_keys=True) + "\n")
PY
fi
"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
trap - EXIT
printf '\nRemote-SSH Valkey parity acceptance passed.\nevidence: %s\n' "$local_evidence"
