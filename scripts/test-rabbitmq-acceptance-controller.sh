#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
controller="$repo_root/scripts/run-rabbitmq-quadlet-acceptance.sh"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  printf 'test-rabbitmq-acceptance-controller: %s\n' "$*" >&2
  exit 1
}

image='docker.io/library/rabbitmq@sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91'
touch "$work_dir/probe"
chmod +x "$work_dir/probe"
common=(
  --base-target base.local
  --instance provision-acceptance
  --snapshot clean
  --guest-target provision-acceptance.local
  --environment lab
  --oci-image "$image"
  --probe-binary "$work_dir/probe"
  --host-proof "$repo_root/scripts/prove-rabbitmq-quadlet-host.sh"
  --qualification-validator "$repo_root/scripts/validate-rabbitmq-qualification.py"
  --approval-record "$repo_root/docs/evidence/2026-09-25-rabbitmq-packaging-approval.json"
  --operator marlinf
  --expected-podman-version 5.7.0+ds2-3build1
  --confirm-disposable-instance provision-acceptance
  --evidence-dir "$work_dir/live-evidence"
)

"$controller" --preview "${common[@]}" > "$work_dir/preview.txt"
grep -Fq 'Snapshot: provision-acceptance/clean' "$work_dir/preview.txt" || fail "preview omits snapshot identity"
grep -Fq "Approval record: $repo_root/docs/evidence/2026-09-25-rabbitmq-packaging-approval.json" "$work_dir/preview.txt" || fail "preview omits approval record"
grep -Fq 'Restores: before prepare and after evidence capture' "$work_dir/preview.txt" || fail "preview omits cleanup behavior"
grep -Fq 'No changes made.' "$work_dir/preview.txt" || fail "preview does not state its safety boundary"

if "$controller" --preview "${common[@]/--confirm-disposable-instance/--bad-option}" >/dev/null 2>&1; then
  fail "an unknown confirmation option was accepted"
fi

bad=("${common[@]}")
for index in "${!bad[@]}"; do
  if [[ "${bad[$index]}" == provision-acceptance && "$index" -gt 0 && "${bad[$((index - 1))]}" == --confirm-disposable-instance ]]; then
    bad[index]=wrong-instance
  fi
done
if "$controller" --preview "${bad[@]}" >/dev/null 2>&1; then
  fail "a mismatched disposable-instance confirmation was accepted"
fi

fake_bin="$work_dir/fake-bin"
mkdir -p "$fake_bin"
cat > "$fake_bin/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_SSH_LOG"
case "$*" in
  *"incus snapshot list provision-acceptance --format json")
    printf '%s\n' '[{"name":"clean","created_at":"2026-09-25T12:59:14.631056345Z","stateful":false}]'
    ;;
  *"bash -s -- provision-acceptance clean")
    cat >/dev/null
    ;;
  *"provision-acceptance.local true")
    ;;
  *"provision-acceptance.local python3 - lab")
    cat >/dev/null
    count=0
    [[ -f "$FAKE_CLEAN_COUNT" ]] && count="$(cat "$FAKE_CLEAN_COUNT")"
    count=$((count + 1))
    printf '%s\n' "$count" > "$FAKE_CLEAN_COUNT"
    printf '{"schemaVersion":"provision.dev/rabbitmq-clean-host-observation/v1alpha1","bootId":"boot-%s","podmanPresent":false,"environmentAccountPresent":false,"issueEvidencePresent":false,"clean":true}\n' "$count"
    ;;
  *"incus exec provision-acceptance -- bash "*" --prepare "*)
    ;;
  *"incus restart provision-acceptance --timeout 60")
    ;;
  *"incus exec provision-acceptance -- bash "*" --verify-after-reboot "*)
    ;;
  *)
    printf 'unexpected fake ssh call: %s\n' "$*" >&2
    exit 1
    ;;
esac
SH
cat > "$fake_bin/scp" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_SCP_LOG"
if [[ "$*" == *":/var/lib/provision/evidence/issue36/."* ]]; then
  destination="${!#}"
  mkdir -p "$destination"
  cp "$FAKE_SUPPORT_OBSERVATION" "$destination/support-observation.json"
fi
SH
chmod +x "$fake_bin/ssh" "$fake_bin/scp"

cat > "$work_dir/support-observation.json" <<'EOF'
{"schemaVersion":"provision.dev/rabbitmq-packaging-qualification/v1alpha2","result":"passed"}
EOF
: > "$work_dir/ssh.log"
: > "$work_dir/scp.log"
run_args=("${common[@]}")
for index in "${!run_args[@]}"; do
  if [[ "${run_args[$index]}" == "$work_dir/live-evidence" ]]; then
    run_args[index]="$work_dir/controller-evidence"
  fi
done
PATH="$fake_bin:$PATH" \
FAKE_SSH_LOG="$work_dir/ssh.log" \
FAKE_SCP_LOG="$work_dir/scp.log" \
FAKE_CLEAN_COUNT="$work_dir/clean-count" \
FAKE_SUPPORT_OBSERVATION="$work_dir/support-observation.json" \
  "$controller" --run "${run_args[@]}" > "$work_dir/run.txt"

python3 - "$work_dir/controller-evidence/controller-provenance.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    result = json.load(handle)
assert result["schemaVersion"] == "provision.dev/rabbitmq-acceptance-controller/v1alpha1"
assert result["result"] == "passed"
assert result["snapshot"] == {
    "createdAt": "2026-09-25T12:59:14.631056345Z",
    "name": "clean",
    "rawObservationDigest": result["snapshot"]["rawObservationDigest"],
    "stateful": False,
}
assert result["restores"] == ["before-prepare", "after-evidence-capture"]
assert result["prePrepareCleanObservation"]["clean"] is True
assert result["postRestoreCleanObservation"]["clean"] is True
assert result["prePrepareCleanObservation"]["bootId"] == "boot-1"
assert result["postRestoreCleanObservation"]["bootId"] == "boot-2"
assert result["qualification"]["schemaVersion"] == "provision.dev/rabbitmq-packaging-qualification/v1alpha2"
assert result["qualification"]["result"] == "passed"
PY
[[ "$(grep -c 'bash -s -- provision-acceptance clean' "$work_dir/ssh.log")" -eq 2 ]] || \
  fail "run did not restore the exact clean snapshot twice"
grep -Fq -- '--prepare' "$work_dir/ssh.log" || fail "run omitted prepare"
grep -Fq -- '--verify-after-reboot' "$work_dir/ssh.log" || fail "run omitted reboot verification"
grep -Fq 'incus restart provision-acceptance --timeout 60' "$work_dir/ssh.log" || \
  fail "run omitted the real restart seam"

printf 'rabbitmq acceptance controller tests passed\n'
