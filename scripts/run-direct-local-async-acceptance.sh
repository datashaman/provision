#!/usr/bin/env bash
# End-to-end disposable-VM acceptance for issue #49.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_SECRET:-$root/work/issue-39-live/rabbitmq-url.secret}"
remote="/tmp/provision-issue49-direct-local"
remote_evidence="/tmp/provision-issue49-evidence"
local_evidence="${PROVISION_ISSUE49_EVIDENCE:-$root/work/issue-49-live/evidence}"
ssh_opts=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #49 direct-local asynchronous acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This restores the clean VM snapshot, runs the Provision CLI on the VM through the local Host Handler, injects local response loss, copies evidence back, and restores the VM snapshot after success.\n'

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" && -f "$secret" ]] || {
  printf 'missing authority or secret files; set PROVISION_ACCEPTANCE_AUTHORITY and PROVISION_ACCEPTANCE_SECRET\n' >&2
  exit 2
}

build_dir="$root/work/issue-49-live"
mkdir -p "$build_dir"
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision" ./cmd/provision
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
    "$root/scripts/test-direct-local-async-host.sh" \
    "$root/scripts/run-direct-local-async-acceptance.sh" \
    "$root/scripts/test-first-scheduled-message-host.sh" \
    "$root/scripts/test-worker-handoff-recovery-host.sh" \
    "$root/scripts/test-schedule-recovery-host.sh" \
    "$root/scripts/direct-local-fault-bin/sudo"
  run_check shellcheck shellcheck \
    "$root/scripts/test-direct-local-async-host.sh" \
    "$root/scripts/run-direct-local-async-acceptance.sh" \
    "$root/scripts/test-first-scheduled-message-host.sh" \
    "$root/scripts/test-worker-handoff-recovery-host.sh" \
    "$root/scripts/test-schedule-recovery-host.sh" \
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
    "schemaVersion": "provision.dev/direct-local-async-acceptance-checks/v1alpha1",
    "sourceVersion": source_version,
    "checks": checks,
}
(check_dir / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

reset_and_bootstrap() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote' ]; then mv '$remote' '$remote.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" \
    "$build_dir/provision" \
    "$build_dir/provision-host-executor" \
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

  reset_and_bootstrap
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths and source identity.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote_destination' ]; then mv '$remote_destination' '$remote_destination.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote_evidence'; cd '$remote' && ./scripts/test-direct-local-async-host.sh --provision ./provision --signing-key ./host-authority.key --secret-file ./$(basename "$secret") --work-dir '$remote_destination' --provision-version '$source_version' --confirm-disposable-host provision-acceptance --scenario '$scenario'"
  mkdir -p "$(dirname "$destination")"
  scp "${scp_opts[@]}" -r "$target_user@$target:$remote_destination" "$destination"
}

if [[ -e "$local_evidence" ]]; then
  mv "$local_evidence" "$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "$(dirname "$local_evidence")"
mkdir -p "$local_evidence"

run_local_checks

run_scenario worker-rollback
run_scenario complete

"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

printf '\nDirect-local asynchronous acceptance passed.\n'
printf 'evidence: %s\n' "$local_evidence"
