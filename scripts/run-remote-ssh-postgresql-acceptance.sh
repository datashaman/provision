#!/usr/bin/env bash
# End-to-end disposable-VM acceptance for issue #76: runs the direct-local
# PostgreSQL matrix and the matching strict-SSH matrix from clean snapshots of
# the same VM, then writes parity evidence and restores the clean snapshot.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
instance="${PROVISION_ACCEPTANCE_INCUS_INSTANCE:-provision-acceptance}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_POSTGRESQL_SECRET:-$root/work/issue-75-live/postgresql-url.secret}"
remote="/tmp/provision-issue76-bootstrap"
local_evidence="${PROVISION_ISSUE76_EVIDENCE:-$root/work/issue-76-live/evidence}"
direct_evidence="$local_evidence/direct-local"
ssh_evidence="$local_evidence/remote-ssh"
build_dir="$root/work/issue-76-live"
fixture_bundle="$root/work/issue-75-live/provision-example-database-http-linux-amd64.tar.gz"
read -r -a modes <<<"${PROVISION_ACCEPTANCE_MODES:-before after undecidable}"
ssh_opts=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #76 remote-SSH PostgreSQL parity acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This runs the direct-local matrix and the matching strict-SSH matrix (%s) from clean VM snapshots, writes parity evidence, and restores the clean snapshot.\n' "${modes[*]}"

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" ]] || {
  printf 'missing authority files; set PROVISION_ACCEPTANCE_AUTHORITY\n' >&2
  exit 2
}

cleanup_needed=false
cleanup() {
  local status=$?
  # PROVISION_ACCEPTANCE_KEEP_VM=1 leaves a failed VM in place for diagnosis.
  if [[ "$cleanup_needed" == true && ( "$status" -eq 0 || -z "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ) ]]; then
    "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

if [[ -e "$local_evidence" ]]; then
  mv "$local_evidence" "$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "$local_evidence" "$build_dir"

source_version="$(git -C "$root" rev-parse HEAD)"
if ! git -C "$root" diff --quiet HEAD --; then
  source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
fi

run_local_checks() {
  local check_dir="$local_evidence/checks"
  mkdir -p "$check_dir"
  run_check() {
    local name="$1"
    shift
    printf '[check] %s\n' "$name"
    set +e
    "$@" >"$check_dir/$name.log" 2>&1
    local status=$?
    set -e
    printf '%s\n' "$status" >"$check_dir/$name.status"
    [[ "$status" == 0 ]] || {
      printf 'check failed: %s; see %s\n' "$name" "$check_dir/$name.log" >&2
      exit "$status"
    }
  }
  local scripts=(
    "$root/scripts/test-remote-ssh-postgresql-host.sh"
    "$root/scripts/run-remote-ssh-postgresql-acceptance.sh"
    "$root/scripts/test-direct-local-postgresql-host.sh"
    "$root/scripts/run-direct-local-postgresql-acceptance.sh"
    "$root/scripts/remote-ssh-fault-bin/ssh"
    "$root/scripts/direct-local-fault-bin/sudo"
  )
  run_check bash-n bash -n "${scripts[@]}"
  run_check shellcheck shellcheck "${scripts[@]}"
  run_check go-test go test ./...
  run_check go-vet go vet ./...
  python3 - "$check_dir" "$source_version" <<'PY'
import json
from pathlib import Path
import sys

check_dir = Path(sys.argv[1])
checks = [
    {"name": p.stem, "exitStatus": int(p.read_text(encoding="utf-8").strip()), "log": f"{p.stem}.log"}
    for p in sorted(check_dir.glob("*.status"))
]
(check_dir / "summary.json").write_text(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-postgresql-acceptance-checks/v1alpha1",
    "sourceVersion": sys.argv[2],
    "checks": checks,
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

reset_and_bootstrap_for_ssh() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  # A freshly restored VM is briefly busy; wait for a quiet 1-minute load average so
  # the Host's bounded (10s) SQL checks are not measured against boot-time contention.
  # shellcheck disable=SC2016 # The awk program runs on the Host, not here.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    'for _ in $(seq 1 60); do awk "{exit !(\$1 < 0.5)}" /proc/loadavg && exit 0; sleep 2; done; exit 0'
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote' ]; then mv '$remote' '$remote.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" \
    "$root/scripts/bootstrap-host.sh" \
    "$build_dir/provision-host-executor" \
    "$authority/host-authority.pub" \
    "$target_user@$target:$remote/"
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "sudo -n bash '$remote/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
}

run_ssh_mode() {
  local mode="$1"
  cleanup_needed=true
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
    --confirm-disposable-host "$instance" \
    --fault-mode "$mode"
}

write_parity_summary() {
  python3 - "$local_evidence/parity-summary.json" "$source_version" "$direct_evidence" "$ssh_evidence" "${modes[@]}" <<'PY'
import json
from pathlib import Path
import sys

destination, source_version, direct_root, ssh_root = sys.argv[1:5]
modes = sys.argv[5:]
direct_root, ssh_root = Path(direct_root), Path(ssh_root)


def load(root, mode, name):
    return json.loads((root / mode / name).read_text(encoding="utf-8"))


comparisons = []
for mode in modes:
    direct_acc, remote_acc = load(direct_root, mode, "accounting.json"), load(ssh_root, mode, "accounting.json")
    direct, remote = load(direct_root, mode, "support-observation.json"), load(ssh_root, mode, "support-observation.json")
    checks = {
        "databaseAccountingMatches": direct_acc == remote_acc,
        "osMatches": (direct["os"], direct["osVersion"], direct["architecture"]) == (remote["os"], remote["osVersion"], remote["architecture"]),
        "systemdMatches": direct["systemdVersion"] == remote["systemdVersion"],
        "podmanMatches": direct["podmanVersion"] == remote["podmanVersion"],
        "executorDigestMatches": direct["executorDigest"] == remote["executorDigest"],
        "postgresqlMatches": direct["postgresql"] == remote["postgresql"],
        "matrixMatches": direct["matrix"] == [m.replace(" over strict SSH", "") for m in remote["matrix"]],
        "strictHostKeyPinned": remote["ssh"]["strictHostKeyChecking"] is True and bool(remote["ssh"]["hostFingerprint"]) and bool(remote["ssh"]["knownHostsEntries"]),
        "passwordAuthenticationDisabled": remote["ssh"]["passwordAuthentication"] is False,
    }
    if not all(checks.values()):
        raise SystemExit(f"{mode} parity check failed: {checks!r}")
    comparisons.append({
        "faultMode": mode,
        "checks": checks,
        "activeGeneration": remote_acc["activeGeneration"]["id"],
        "retainedGenerations": [g["id"] for g in remote_acc["retainedGenerations"]],
        "workloadRecords": remote_acc["workloadRecords"]["records"],
        "controller": remote["controller"],
        "ssh": remote["ssh"],
        "hostTarget": {k: remote[k] for k in ("os", "osVersion", "architecture", "systemdVersion", "podmanVersion", "executorDigest")},
        "provisionBinarySha256": {"directLocal": direct["provisionBinarySha256"], "remote": remote["provisionBinarySha256"]},
        "postgresql": remote["postgresql"],
    })

Path(destination).write_text(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-postgresql-parity/v1alpha1",
    "sourceVersion": source_version,
    "directLocalEvidence": str(direct_root),
    "remoteSshEvidence": str(ssh_root),
    "comparisons": comparisons,
    "supportQualification": {
        "qualified": [
            "one Ubuntu 26.04 x86_64 disposable VM with systemd 259 and rootless Podman/Quadlet",
            "managed single-node PostgreSQL 17.6 provisioned through the CLI, then forward-only Store Transition to PostgreSQL 17.7 at the pinned image manifests",
            "strict OpenSSH management transport with a pinned host key, equivalent to direct-local behavior",
            "transport loss before Host dispatch, after Host completion, and with unreadable authority-switch evidence around generation creation, synchronization, write fence, authority switch, backup, restore verification, and retention marking",
        ],
        "excluded": [
            "RDS", "Valkey", "object stores", "multi-node PostgreSQL availability", "production disaster recovery",
            "external PostgreSQL adoption", "automatic cleanup", "untested host, runtime, PostgreSQL, or Podman versions",
        ],
    },
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

run_local_checks

controller_goos="$(go env GOOS)"
controller_goarch="$(go env GOARCH)"
GOOS="$controller_goos" GOARCH="$controller_goarch" go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor

# The direct-local wrapper builds the fixture bundle and resets the VM on success.
PROVISION_ISSUE75_EVIDENCE="$direct_evidence" \
PROVISION_ACCEPTANCE_TARGET="$target" \
PROVISION_ACCEPTANCE_USER="$target_user" \
PROVISION_ACCEPTANCE_AUTHORITY="$authority" \
PROVISION_ACCEPTANCE_POSTGRESQL_SECRET="$secret" \
PROVISION_ACCEPTANCE_MODES="${modes[*]}" \
  "$root/scripts/run-direct-local-postgresql-acceptance.sh"

for mode in "${modes[@]}"; do
  run_ssh_mode "$mode"
done
write_parity_summary

"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
cleanup_needed=false
trap - EXIT

printf '\nRemote-SSH PostgreSQL parity acceptance passed.\n'
printf 'evidence: %s\n' "$local_evidence"
