#!/usr/bin/env bash
# End-to-end disposable-VM acceptance for issue #50.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
incus_host="${PROVISION_ACCEPTANCE_INCUS_HOST:-base.local}"
incus_instance="${PROVISION_ACCEPTANCE_INCUS_INSTANCE:-provision-acceptance}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_SECRET:-$root/work/issue-39-live/rabbitmq-url.secret}"
remote="/tmp/provision-issue50-bootstrap"
remote_evidence="/tmp/provision-issue50-evidence"
local_evidence="${PROVISION_ISSUE50_EVIDENCE:-$root/work/issue-50-live/evidence}"
direct_evidence="$local_evidence/direct-local"
ssh_evidence="$local_evidence/remote-ssh"
build_dir="$root/work/issue-50-live"
ssh_opts=(-o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #50 remote-SSH asynchronous parity acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This runs the direct-local async matrix and the matching strict-SSH matrix from clean VM snapshots, then writes parity evidence and restores the clean snapshot.\n'

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" && -f "$secret" ]] || {
  printf 'missing authority or secret files; set PROVISION_ACCEPTANCE_AUTHORITY and PROVISION_ACCEPTANCE_SECRET\n' >&2
  exit 2
}
[[ -x "$root/scripts/run-direct-local-async-acceptance.sh" ]] || {
  printf 'direct-local async wrapper is not executable\n' >&2
  exit 2
}

mkdir -p "$build_dir"
controller_goos="$(go env GOOS)"
controller_goarch="$(go env GOARCH)"
GOOS="$controller_goos" GOARCH="$controller_goarch" go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
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

  run_markdown_link_check() {
    local name="markdown-links"
    printf '[check] %s\n' "$name"
    set +e
    python3 - "$root" >"$check_dir/$name.log" 2>&1 <<'PY'
from pathlib import Path
import re
import sys
from urllib.parse import unquote

root = Path(sys.argv[1])
missing = []
link_re = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
for path in sorted(root.rglob("*.md")):
    if any(part in {".git", "work"} for part in path.parts):
        continue
    text = path.read_text(encoding="utf-8")
    for raw in link_re.findall(text):
        target = raw.split()[0].strip("<>")
        if not target or target.startswith(("#", "http://", "https://", "mailto:")):
            continue
        target_path = target.split("#", 1)[0]
        if not target_path:
            continue
        candidate = (path.parent / unquote(target_path)).resolve()
        try:
            candidate.relative_to(root.resolve())
        except ValueError:
            continue
        if not candidate.exists():
            missing.append(f"{path.relative_to(root)} -> {target}")
if missing:
    print("\n".join(missing))
    raise SystemExit(1)
print("checked markdown relative links")
PY
    local status=$?
    set -e
    printf '%s\n' "$status" >"$check_dir/$name.status"
    [[ "$status" == 0 ]] || {
      printf 'check failed: %s; see %s\n' "$name" "$check_dir/$name.log" >&2
      exit "$status"
    }
  }

  run_check bash-n bash -n \
    "$root/scripts/test-remote-ssh-async-host.sh" \
    "$root/scripts/run-remote-ssh-async-acceptance.sh" \
    "$root/scripts/test-direct-local-async-host.sh" \
    "$root/scripts/run-direct-local-async-acceptance.sh" \
    "$root/scripts/test-worker-handoff-recovery-host.sh" \
    "$root/scripts/test-schedule-recovery-host.sh" \
    "$root/scripts/remote-ssh-fault-bin/ssh" \
    "$root/scripts/direct-local-fault-bin/sudo"
  run_check shellcheck shellcheck \
    "$root/scripts/test-remote-ssh-async-host.sh" \
    "$root/scripts/run-remote-ssh-async-acceptance.sh" \
    "$root/scripts/test-direct-local-async-host.sh" \
    "$root/scripts/run-direct-local-async-acceptance.sh" \
    "$root/scripts/test-worker-handoff-recovery-host.sh" \
    "$root/scripts/test-schedule-recovery-host.sh" \
    "$root/scripts/remote-ssh-fault-bin/ssh" \
    "$root/scripts/direct-local-fault-bin/sudo"
  run_check go-test go test ./...
  run_check go-vet go vet ./...
  run_check go-test-race go test -race ./...
  run_markdown_link_check

  python3 - "$check_dir" "$source_version" <<'PY'
import json
from pathlib import Path
import sys

check_dir = Path(sys.argv[1])
source_version = sys.argv[2]
checks = []
for status_path in sorted(check_dir.glob("*.status")):
    name = status_path.stem
    checks.append({
        "name": name,
        "exitStatus": int(status_path.read_text(encoding="utf-8").strip()),
        "log": f"{name}.log",
    })
summary = {
    "schemaVersion": "provision.dev/remote-ssh-async-acceptance-checks/v1alpha1",
    "sourceVersion": source_version,
    "checks": checks,
}
(check_dir / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

reset_and_bootstrap_for_ssh() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

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

run_ssh_scenario() {
  local scenario="$1"
  local destination="$ssh_evidence/$scenario"
  local remote_destination="$remote_evidence/$scenario"

  mkdir -p "$(dirname "$destination")"
  reset_and_bootstrap_for_ssh
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote_destination' ]; then mv '$remote_destination' '$remote_destination.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote_evidence'"

  "$root/scripts/test-remote-ssh-async-host.sh" \
    --provision "$build_dir/provision" \
    --signing-key "$authority/host-authority.key" \
    --secret-file "$secret" \
    --work-dir "$destination" \
    --provision-version "$source_version" \
    --target "$target" \
    --target-user "$target_user" \
    --confirm-disposable-host "$incus_instance" \
    --scenario "$scenario" \
    --incus-host "$incus_host" \
    --incus-instance "$incus_instance"
}

write_parity_summary() {
  python3 - "$local_evidence/parity-summary.json" "$source_version" "$direct_evidence" "$ssh_evidence" <<'PY'
import json
from pathlib import Path
import sys

destination = Path(sys.argv[1])
source_version = sys.argv[2]
direct_root = Path(sys.argv[3])
ssh_root = Path(sys.argv[4])
scenarios = ["worker-rollback", "complete"]

def load_summary(root, scenario):
    path = root / scenario / "matrix-summary.json"
    if not path.exists():
        raise SystemExit(f"missing matrix summary: {path}")
    return json.loads(path.read_text(encoding="utf-8"))

comparisons = []
for scenario in scenarios:
    direct = load_summary(direct_root, scenario)
    remote = load_summary(ssh_root, scenario)
    direct_host = direct.get("host", {})
    remote_host = remote.get("host", {})
    direct_async = direct.get("async", {})
    remote_async = remote.get("async", {})
    direct_messages = direct.get("messageAccounting", [])
    remote_messages = remote.get("messageAccounting", [])
    direct_occurrences = direct.get("occurrenceAccounting", [])
    remote_occurrences = remote.get("occurrenceAccounting", [])
    def evidence_slot(path):
        return path.replace(".resumed.json", ".json") if path else path
    def message_shape(messages):
        return sorted(json.dumps({
            "slot": evidence_slot(message.get("path")),
            "acknowledged": message.get("acknowledged"),
            "status": message.get("status"),
            "operationOutcome": message.get("operationOutcome"),
        }, sort_keys=True) for message in messages)
    def occurrence_shape(occurrences):
        return sorted(json.dumps({
            "slot": evidence_slot(occurrence.get("path")),
            "taskGenerationId": occurrence.get("taskGenerationId"),
            "disposition": occurrence.get("disposition"),
            "invocationOutcome": occurrence.get("invocationOutcome"),
        }, sort_keys=True) for occurrence in occurrences)
    direct_invocation_ids = sorted(occurrence.get("taskInvocationId") for occurrence in direct_occurrences if occurrence.get("taskInvocationId"))
    remote_invocation_ids = sorted(occurrence.get("taskInvocationId") for occurrence in remote_occurrences if occurrence.get("taskInvocationId"))
    checks = {
        "hostOsMatches": direct_host.get("os") == remote_host.get("os"),
        "hostVersionMatches": direct_host.get("osVersion") == remote_host.get("osVersion"),
        "hostArchitectureMatches": direct_host.get("architecture") == remote_host.get("architecture"),
        "systemdMatches": direct_host.get("systemdVersion") == remote_host.get("systemdVersion"),
        "executorDigestMatches": direct_host.get("executorDigest") == remote_host.get("executorDigest"),
        "rabbitmqManifestMatches": direct_async.get("rabbitmqImageManifest") == remote_async.get("rabbitmqImageManifest"),
        "scheduleAppletDigestMatches": direct_async.get("scheduleAppletDigest") == remote_async.get("scheduleAppletDigest"),
        "messageObservationCountMatches": len(direct_messages) == len(remote_messages),
        "occurrenceObservationCountMatches": len(direct_occurrences) == len(remote_occurrences),
        "messageAccountingShapeMatches": message_shape(direct_messages) == message_shape(remote_messages),
        "occurrenceAccountingShapeMatches": occurrence_shape(direct_occurrences) == occurrence_shape(remote_occurrences),
        "invocationIdentitiesRecorded": len(direct_invocation_ids) == len(direct_occurrences) and len(remote_invocation_ids) == len(remote_occurrences),
        "activeGenerationAccountingMatches": direct.get("activeGenerationAccounting") == remote.get("activeGenerationAccounting"),
        "retainedGenerationAccountingMatches": direct.get("retainedGenerationAccounting") == remote.get("retainedGenerationAccounting"),
        "ownedResourceInventoryMatches": direct.get("ownedResourceInventory") == remote.get("ownedResourceInventory"),
        "artifactVersionsMatch": direct.get("artifactVersions") == remote.get("artifactVersions"),
    }
    if not all(checks.values()):
        raise SystemExit(f"{scenario} parity check failed: {checks!r}")
    comparisons.append({
        "scenario": scenario,
        "checks": checks,
        "directMessages": len(direct_messages),
        "remoteMessages": len(remote_messages),
        "directOccurrences": len(direct_occurrences),
        "remoteOccurrences": len(remote_occurrences),
        "directInvocationIdentities": direct_invocation_ids,
        "remoteInvocationIdentities": remote_invocation_ids,
        "activeGenerations": remote.get("activeGenerationAccounting"),
        "retainedGenerations": remote.get("retainedGenerationAccounting"),
        "ownedResourceInventory": remote.get("ownedResourceInventory"),
        "artifactVersions": remote.get("artifactVersions"),
        "remoteTransportFaults": remote.get("matrix", {}),
    })

destination.write_text(json.dumps({
    "schemaVersion": "provision.dev/remote-ssh-async-parity/v1alpha1",
    "sourceVersion": source_version,
    "directLocalEvidence": str(direct_root),
    "remoteSshEvidence": str(ssh_root),
    "comparisons": comparisons,
    "supportQualification": {
        "qualified": [
            "one Ubuntu 26.04 x86_64 disposable VM",
            "systemd 259 with rootless Podman/Quadlet",
            "RabbitMQ 4.3.6 at the pinned image manifest",
            "strict OpenSSH management transport with pinned host key",
            "one managed Queue, one systemd Worker, one systemd Task, one stable Schedule",
            "controller response loss around Queue preparation, Worker fencing and activation, Schedule handoff, and Task delivery",
        ],
        "excluded": [
            "Queue Generation migration",
            "exactly-once delivery or processing",
            "autoscaling",
            "other Host OS or dependency versions",
            "AWS, ECS, Lambda, realtime servers, stores, or store transitions",
            "Host or RabbitMQ data-loss recovery",
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

PROVISION_ISSUE49_EVIDENCE="$direct_evidence" \
PROVISION_ACCEPTANCE_TARGET="$target" \
PROVISION_ACCEPTANCE_USER="$target_user" \
PROVISION_ACCEPTANCE_AUTHORITY="$authority" \
PROVISION_ACCEPTANCE_SECRET="$secret" \
  "$root/scripts/run-direct-local-async-acceptance.sh"

run_ssh_scenario worker-rollback
run_ssh_scenario complete
write_parity_summary

"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

printf '\nRemote-SSH asynchronous parity acceptance passed.\n'
printf 'evidence: %s\n' "$local_evidence"
