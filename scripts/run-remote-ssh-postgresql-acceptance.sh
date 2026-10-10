#!/usr/bin/env bash
# End-to-end disposable-VM acceptance for issue #76: direct-local and strict-SSH
# PostgreSQL matrices from identical clean snapshots, then a parity comparison.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
host_name="${PROVISION_ACCEPTANCE_HOSTNAME:-provision-acceptance}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_POSTGRESQL_SECRET:-$root/work/issue-75-live/postgresql-url.secret}"
local_evidence="${PROVISION_ISSUE76_EVIDENCE:-$root/work/issue-76-live/evidence}"
direct_evidence="$local_evidence/direct-local"
ssh_evidence="$local_evidence/remote-ssh"
build_dir="$root/work/issue-76-live"
remote="/tmp/provision-issue76-bootstrap"
modes="${PROVISION_ACCEPTANCE_MODES:-before after undecidable}"
read -ra mode_list <<<"$modes"
ssh_opts=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #76 remote-SSH PostgreSQL parity acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This runs the direct-local matrix and the matching strict-SSH matrix from clean VM snapshots per fault mode, writes parity evidence, and restores the clean snapshot.\n'

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" && -f "$secret" && ! -L "$secret" ]] || {
  printf 'missing authority or secret files; set PROVISION_ACCEPTANCE_AUTHORITY and PROVISION_ACCEPTANCE_POSTGRESQL_SECRET\n' >&2
  exit 2
}

cleanup() {
  local status=$?
  # PROVISION_ACCEPTANCE_KEEP_VM=1 leaves a failed VM in place for diagnosis.
  if [[ "$status" -ne 0 && -z "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]]; then
    "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

mkdir -p "$build_dir"
GOOS="$(go env GOOS)" GOARCH="$(go env GOARCH)" go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)"
if ! git -C "$root" diff --quiet HEAD --; then
  source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
fi
fixture_bundle="$build_dir/provision-example-database-http-linux-amd64.tar.gz"

run_local_checks() {
  local check_dir="$local_evidence/checks"
  mkdir -p "$check_dir"
  local scripts=("$root/scripts/test-remote-ssh-postgresql-host.sh" "$root/scripts/run-remote-ssh-postgresql-acceptance.sh" "$root/scripts/test-direct-local-postgresql-host.sh" "$root/scripts/run-direct-local-postgresql-acceptance.sh" "$root/scripts/remote-ssh-fault-bin/ssh" "$root/scripts/direct-local-fault-bin/sudo")
  run_check() {
    local name="$1"; shift
    printf '[check] %s\n' "$name"
    set +e
    "$@" >"$check_dir/$name.log" 2>&1
    local status=$?
    set -e
    printf '%s\n' "$status" >"$check_dir/$name.status"
    [[ "$status" == 0 ]] || { printf 'check failed: %s; see %s\n' "$name" "$check_dir/$name.log" >&2; exit "$status"; }
  }
  run_check bash-n bash -n "${scripts[@]}"
  run_check shellcheck shellcheck "${scripts[@]}"
  run_check go-test-database-planner go test ./cmd/provision -run TestDatabasePlanPreviewIsDeterministicAndFailsClosedForUnqualifiedTransitions
  python3 - "$check_dir" "$source_version" <<'PY'
import json
from pathlib import Path
import sys

check_dir, source_version = Path(sys.argv[1]), sys.argv[2]
checks = [{"name": p.stem, "exitStatus": int(p.read_text().strip()), "log": f"{p.stem}.log"} for p in sorted(check_dir.glob("*.status"))]
(check_dir / "summary.json").write_text(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-postgresql-acceptance-checks/v1alpha1",
    "sourceVersion": source_version,
    "checks": checks,
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

reset_and_bootstrap_for_ssh() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote' ]; then mv '$remote' '$remote.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" "$root/scripts/bootstrap-host.sh" "$build_dir/provision-host-executor" "$authority/host-authority.pub" "$target_user@$target:$remote/"
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "sudo -n bash '$remote/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
}

run_ssh_mode() {
  local mode="$1"
  mkdir -p "$ssh_evidence"
  reset_and_bootstrap_for_ssh
  "$root/scripts/test-remote-ssh-postgresql-host.sh" \
    --provision "$build_dir/provision" \
    --signing-key "$authority/host-authority.key" \
    --secret-file "$secret" \
    --artifact-bundle "$fixture_bundle" \
    --work-dir "$ssh_evidence/$mode" \
    --provision-version "$source_version" \
    --target "$target" \
    --target-user "$target_user" \
    --confirm-disposable-host "$host_name" \
    --fault-mode "$mode"
}

write_parity_summary() {
  python3 - "$local_evidence/parity-summary.json" "$source_version" "$direct_evidence" "$ssh_evidence" "${mode_list[@]}" <<'PY'
import json
from pathlib import Path
import sys

destination, source_version, direct_root, ssh_root = Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]), Path(sys.argv[4])
comparisons = []
for mode in sys.argv[5:]:
    direct = json.loads((direct_root / mode / "support-observation.json").read_text(encoding="utf-8"))
    remote = json.loads((ssh_root / mode / "support-observation.json").read_text(encoding="utf-8"))
    podman = [json.loads((root / mode / "transition-final-host.json").read_text(encoding="utf-8"))["database"]["capabilities"]["podmanVersion"] for root in (direct_root, ssh_root)]
    checks = {
        "hostPlatformMatches": all(direct[k] == remote[k] for k in ("os", "osVersion", "architecture", "systemdVersion", "kernel")),
        "executorDigestMatches": direct["executorDigest"] == remote["executorDigest"],
        "postgresqlMatches": direct["postgresql"] == remote["postgresql"],
        "podmanMatches": podman[0] == podman[1],
        "accountingMatches": direct["accounting"] == remote["accounting"],
        "remoteIdentityPinned": bool(remote["ssh"]["hostFingerprint"]) and bool(remote["ssh"]["knownHostsEntries"]) and remote["ssh"]["strictHostKeyChecking"],
    }
    if not all(checks.values()):
        raise SystemExit(f"{mode} parity check failed: {checks!r}")
    comparisons.append({
        "faultMode": mode,
        "checks": checks,
        "controller": remote["controller"],
        "ssh": remote["ssh"],
        "versions": {
            "provisionSource": remote["provisionSourceVersion"],
            "provisionControllerBinary": remote["provisionBinarySha256"],
            "directLocalProvisionBinary": direct["provisionBinarySha256"],
            "executor": remote["executorDigest"],
            "postgresql": remote["postgresql"]["version"],
            "postgresqlImageManifest": remote["postgresql"]["imageManifest"],
            "podman": podman[1],
            "systemd": remote["systemdVersion"],
            "topology": remote["postgresql"]["topology"],
        },
        "accounting": remote["accounting"],
    })
destination.write_text(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-postgresql-parity/v1alpha1",
    "sourceVersion": source_version,
    "comparisons": comparisons,
    "supportQualification": {
        "qualified": [
            "exactly the combination recorded in each comparison: controller platform, Host Target platform, pinned SSH host key, Provision, executor, PostgreSQL, Podman, systemd, and single-node topology",
            "managed host-local PostgreSQL provisioning, forward-only same-major Store Transition, backup, isolated restore verification, and retention marking over strict SSH",
        ],
        "excluded": [
            "RDS", "Valkey", "object stores", "multi-node PostgreSQL availability", "production disaster recovery",
            "external PostgreSQL adoption", "automatic cleanup", "untested host or runtime versions",
        ],
    },
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

if [[ -e "$local_evidence" ]]; then
  mv "$local_evidence" "$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "$local_evidence"

run_local_checks
# PROVISION_ACCEPTANCE_SKIP_DIRECT=1 runs only the remote leg for diagnosis; it writes no parity evidence.
if [[ -z "${PROVISION_ACCEPTANCE_SKIP_DIRECT:-}" ]]; then
  PROVISION_ISSUE75_EVIDENCE="$direct_evidence" \
  PROVISION_ACCEPTANCE_TARGET="$target" \
  PROVISION_ACCEPTANCE_USER="$target_user" \
  PROVISION_ACCEPTANCE_AUTHORITY="$authority" \
  PROVISION_ACCEPTANCE_POSTGRESQL_SECRET="$secret" \
  PROVISION_ACCEPTANCE_MODES="$modes" \
    "$root/scripts/run-direct-local-postgresql-acceptance.sh"
fi

# The direct-local wrapper builds the fixture bundle under its own work directory.
fixture_bundle="$root/work/issue-75-live/provision-example-database-http-linux-amd64.tar.gz"
[[ -f "$fixture_bundle" ]] || { printf 'missing fixture bundle %s; run the direct-local wrapper once first\n' "$fixture_bundle" >&2; exit 2; }
for mode in "${mode_list[@]}"; do
  run_ssh_mode "$mode"
done
[[ -n "${PROVISION_ACCEPTANCE_SKIP_DIRECT:-}" ]] || write_parity_summary
"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
trap - EXIT

printf '\nRemote-SSH PostgreSQL parity acceptance passed.\n'
printf 'evidence: %s\n' "$local_evidence"
