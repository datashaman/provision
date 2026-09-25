#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
inspector="$repo_root/scripts/inspect-rabbitmq-packaging.sh"
fixture_root="$(mktemp -d)"
trap 'rm -rf "$fixture_root"' EXIT

fail() {
  printf 'test-rabbitmq-packaging-inspection: %s\n' "$*" >&2
  exit 1
}

mkdir -p "$fixture_root/bin"
cat > "$fixture_root/bin/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$PROVISION_TEST_SSH_ARGS"
cat > "$PROVISION_TEST_REMOTE_PROGRAM"
printf '%s\n' '{"schemaVersion":"provision.dev/rabbitmq-packaging-observation/v1alpha1","decisionStatus":"awaiting-human-approval","mutatingOperationsEnabled":false}'
EOF
chmod +x "$fixture_root/bin/ssh"

export PROVISION_TEST_SSH_ARGS="$fixture_root/ssh-args"
export PROVISION_TEST_REMOTE_PROGRAM="$fixture_root/remote-program"
image='docker.io/library/rabbitmq@sha256:8196399ba16a67aad44a2eb2cd66d16d72f22bd2d1d55f0e3152df841969cecf'

PATH="$fixture_root/bin:$PATH" "$inspector" \
  --target marlinf@provision-acceptance.local \
  --environment lab \
  --oci-image "$image" > "$fixture_root/result.json"

python3 - "$fixture_root/result.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    result = json.load(handle)

assert result["schemaVersion"] == "provision.dev/rabbitmq-packaging-observation/v1alpha1"
assert result["decisionStatus"] == "awaiting-human-approval"
assert result["mutatingOperationsEnabled"] is False
PY

grep -Fxq 'BatchMode=yes' "$fixture_root/ssh-args" || fail "SSH must be non-interactive"
grep -Fxq 'StrictHostKeyChecking=yes' "$fixture_root/ssh-args" || fail "SSH must pin the existing host identity"
grep -Fq 'apt-cache' "$fixture_root/remote-program" || fail "native package observation is missing"
grep -Fq 'podman' "$fixture_root/remote-program" || fail "OCI capability observation is missing"
if grep -Eq 'apt-get[[:space:]]+install|systemctl[[:space:]]+(enable|start|restart)|podman[[:space:]]+pull' "$fixture_root/remote-program"; then
  fail "inspection payload contains a mutating command"
fi

if "$inspector" --target local --environment lab --oci-image rabbitmq:4.0.5 >/dev/null 2>&1; then
  fail "a mutable OCI tag was accepted"
fi

if "$inspector" --target local --environment 'lab;touch /tmp/nope' --oci-image "$image" >/dev/null 2>&1; then
  fail "an unsafe Environment identity was accepted"
fi

if PATH="$fixture_root/bin:$PATH" "$inspector" --target '-oProxyCommand=touch_bad' --environment lab --oci-image "$image" >/dev/null 2>&1; then
  fail "an option-shaped SSH target was accepted"
fi

printf 'rabbitmq packaging inspection tests passed\n'
