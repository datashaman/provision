#!/usr/bin/env bash
# End-to-end disposable-VM acceptance for issue #48.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
incus_host="${PROVISION_ACCEPTANCE_INCUS_HOST:-base.local}"
incus_instance="${PROVISION_ACCEPTANCE_INCUS_INSTANCE:-provision-acceptance}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_SECRET:-$root/work/issue-39-live/rabbitmq-url.secret}"
remote="/tmp/provision-issue48-bootstrap"

printf 'Provision issue #48 automated live acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This restores the clean VM snapshot, establishes an async baseline, hands the Schedule to a new Task generation, injects lost responses, and reboots the VM.\n'

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" && -f "$secret" ]] || {
  printf 'missing authority or secret files; set PROVISION_ACCEPTANCE_AUTHORITY and PROVISION_ACCEPTANCE_SECRET\n' >&2
  exit 2
}

build_dir="$root/work/issue-48-live"
mkdir -p "$build_dir"
GOOS=darwin GOARCH=arm64 go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor

config_dir="$(mktemp -d /tmp/provision-issue48-config.XXXXXX)"
cp "$root/examples/host-async/application.yaml" "$config_dir/application.yaml"
cp "$root/examples/host-async/environment.yaml" "$config_dir/environment.yaml"
cp "$root/examples/host-async/revision.yaml" "$config_dir/revision.yaml"
cp "$root/examples/host-async/revision-task-v2.yaml" "$config_dir/revision-task-v2.yaml"
cp "$root/examples/host-async/root.yaml" "$config_dir/root.yaml"
cp "$root/examples/host-async/root-task-v2.yaml" "$config_dir/root-task-v2.yaml"
python3 - "$config_dir/environment.yaml" "$target" "$target_user" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
data = path.read_text(encoding="utf-8")
data = data.replace("address: base.local", f"address: {sys.argv[2]}")
data = data.replace("user: marlinf", f"user: {sys.argv[3]}")
path.write_text(data, encoding="utf-8")
PY

"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
  "mkdir -p '$remote' && chmod 0700 '$remote'"
scp -q -o StrictHostKeyChecking=yes \
  "$root/scripts/bootstrap-host.sh" \
  "$build_dir/provision-host-executor" \
  "$authority/host-authority.pub" \
  "$target_user@$target:$remote/"
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" \
  "sudo -n bash '$remote/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"

baseline="/tmp/provision-issue48-baseline"
if [[ -e "$baseline" ]]; then
  mv "$baseline" "$baseline.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
"$root/scripts/test-first-scheduled-message-host.sh" \
  --provision "$build_dir/provision" \
  --signing-key "$authority/host-authority.key" \
  --config "$config_dir/root.yaml" \
  --secret-file "$secret" \
  --work-dir "$baseline" \
  --target "$target" \
  --target-user "$target_user"

evidence="/tmp/provision-issue48-evidence"
if [[ -e "$evidence" ]]; then
  mv "$evidence" "$evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
"$root/scripts/test-schedule-recovery-host.sh" \
  --provision "$build_dir/provision" \
  --signing-key "$authority/host-authority.key" \
  --config "$config_dir/root-task-v2.yaml" \
  --secret-file "$secret" \
  --state-seed "$baseline/state.db" \
  --work-dir "$evidence" \
  --target "$target" \
  --target-user "$target_user" \
  --incus-host "$incus_host" \
  --incus-instance "$incus_instance"

printf '\nSchedule recovery acceptance passed.\n'
