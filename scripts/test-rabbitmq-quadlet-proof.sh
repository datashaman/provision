#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
proof="$repo_root/scripts/prove-rabbitmq-quadlet-host.sh"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  printf 'test-rabbitmq-quadlet-proof: %s\n' "$*" >&2
  exit 1
}

image='docker.io/library/rabbitmq@sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91'
approval="$work_dir/approval.json"
cat > "$approval" <<EOF
{
  "schemaVersion": "provision.dev/rabbitmq-packaging-approval/v1alpha1",
  "issue": 36,
  "decision": "digest-pinned-rootless-oci-quadlet",
  "environment": "lab",
  "operator": "marlinf",
  "product": {"name": "RabbitMQ", "version": "4.3.6"},
  "image": "$image",
  "topology": {
    "brokerNodes": 1,
    "queueType": "quorum",
    "queueMembers": 1,
    "hostFailureTolerance": 0
  },
  "source": {
    "kind": "codex-thread",
    "context": "Explicit operator approval preceded the live issue 36 acceptance mutation."
  }
}
EOF
common=(
  --environment lab
  --oci-image "$image"
  --probe-binary "$work_dir/probe"
  --qualification-validator "$repo_root/scripts/validate-rabbitmq-qualification.py"
  --expected-podman-version 5.7.0+ds2-3build1
  --confirm-disposable-host provision-acceptance
  --approval-record "$approval"
  --operator marlinf
)

"$proof" --preview "${common[@]}" > "$work_dir/preview.txt"
grep -Fq 'provision-lab (rootless)' "$work_dir/preview.txt" || fail "preview omits service identity"
grep -Fq "$image" "$work_dir/preview.txt" || fail "preview omits immutable image identity"
grep -Fq 'Approval: marlinf via codex-thread' "$work_dir/preview.txt" || fail "preview omits bound approval provenance"
grep -Fq 'No changes made.' "$work_dir/preview.txt" || fail "preview does not state its safety boundary"

if "$proof" --preview --environment lab --oci-image rabbitmq:4.3.6 \
  --probe-binary "$work_dir/probe" --expected-podman-version 5.7.0+ds2-3build1 \
  --qualification-validator "$repo_root/scripts/validate-rabbitmq-qualification.py" \
  --confirm-disposable-host provision-acceptance --approval-record "$approval" \
  --operator marlinf >/dev/null 2>&1; then
  fail "a mutable image tag was accepted"
fi

if "$proof" --prepare --environment lab --oci-image "$image" \
  --probe-binary "$work_dir/probe" --expected-podman-version 5.7.0+ds2-3build1 \
  --qualification-validator "$repo_root/scripts/validate-rabbitmq-qualification.py" \
  --confirm-disposable-host provision-acceptance --operator marlinf >/dev/null 2>&1; then
  fail "prepare accepted no approval record"
fi

wrong_approval="$work_dir/wrong-approval.json"
sed 's/"environment": "lab"/"environment": "prod"/' "$approval" > "$wrong_approval"
if "$proof" --preview --environment lab --oci-image "$image" \
  --probe-binary "$work_dir/probe" --expected-podman-version 5.7.0+ds2-3build1 \
  --qualification-validator "$repo_root/scripts/validate-rabbitmq-qualification.py" \
  --confirm-disposable-host provision-acceptance --approval-record "$wrong_approval" \
  --operator marlinf >/dev/null 2>&1; then
  fail "an approval for another Environment was accepted"
fi

if "$proof" --prepare --environment lab --oci-image "$image" \
  --probe-binary "$work_dir/probe" --expected-podman-version 5.7.0+ds2-3build1 \
  --qualification-validator "$repo_root/scripts/validate-rabbitmq-qualification.py" \
  --confirm-disposable-host wrong-host --approval-record "$approval" \
  --operator marlinf >/dev/null 2>&1; then
  fail "a mismatched disposable-host confirmation was accepted"
fi

printf 'rabbitmq Quadlet proof harness tests passed\n'
