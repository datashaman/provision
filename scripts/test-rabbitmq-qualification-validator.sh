#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
validator="$repo_root/scripts/validate-rabbitmq-qualification.py"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  printf 'test-rabbitmq-qualification-validator: %s\n' "$*" >&2
  exit 1
}

image='docker.io/library/rabbitmq@sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91'
config='sha256:5e82332c48dd5c5fe3299ab7ed6550d860801cf9a0330d262d57cadee1ac7899'
fixture="$work_dir/evidence"
mkdir -p "$fixture"

cat > "$fixture/approval-record.json" <<EOF
{"schemaVersion":"provision.dev/rabbitmq-packaging-approval/v1alpha1","issue":36,"decision":"digest-pinned-rootless-oci-quadlet","environment":"lab","operator":"marlinf","product":{"name":"RabbitMQ","version":"4.3.6"},"image":"$image","topology":{"brokerNodes":1,"queueType":"quorum","queueMembers":1,"hostFailureTolerance":0},"source":{"kind":"codex-thread","context":"Explicit operator approval preceded live mutation."}}
EOF
cat > "$fixture/before-reboot-roundtrip.json" <<'EOF'
{"publisherConfirmed":true,"consumerAcknowledged":true,"messageId":"roundtrip","messagesAfterOperation":0}
EOF
cat > "$fixture/before-reboot-persistent.json" <<'EOF'
{"publisherConfirmed":true,"consumerAcknowledged":false,"messageId":"issue36-reboot-survival","messagesAfterOperation":1}
EOF
cat > "$fixture/before-reboot-redelivery.json" <<'EOF'
{"publisherConfirmed":true,"consumerAcknowledged":true,"redelivered":true,"messageId":"issue36-redelivery","messagesAfterOperation":0}
EOF
cat > "$fixture/after-reboot-consume.json" <<'EOF'
{"publisherConfirmed":false,"consumerAcknowledged":true,"messageId":"issue36-reboot-survival","messagesAfterOperation":0}
EOF
cat > "$fixture/after-reboot-queues.json" <<'EOF'
[{"messages":0,"name":"provision-issue36","type":"quorum","durable":true}]
EOF
cat > "$fixture/after-reboot-quorum-members.json" <<'EOF'
{"queue":"provision-issue36","members":["rabbit@provision-lab-rabbitmq"]}
EOF
cat > "$fixture/after-reboot-container.json" <<EOF
[{"Name":"provision-lab-rabbitmq","Image":"$config","ImageName":"$image","ImageDigest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91","State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}]
EOF
cat > "$fixture/after-reboot-image.json" <<EOF
[{"Id":"$config","Digest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91","RepoDigests":["$image"]}]
EOF
cat > "$fixture/after-reboot-cluster.json" <<'EOF'
{"cluster_name":"rabbit@bef69a761309","disk_nodes":["rabbit@provision-lab-rabbitmq"],"running_nodes":["rabbit@provision-lab-rabbitmq"],"versions":{"rabbit@provision-lab-rabbitmq":{"rabbitmq":"4.3.6"}}}
EOF
cat > "$fixture/after-reboot-features.json" <<'EOF'
[{"name":"quorum_queue","state":"enabled"}]
EOF
cat > "$fixture/after-reboot-alarms.json" <<'EOF'
{"localAlarmCheckPassed":true}
EOF
cat > "$fixture/after-reboot-ping.json" <<'EOF'
{"diagnosticsPingPassed":true}
EOF
printf '%s\n' 'provision-lab:x:999:999::/var/lib/provision/runtime/lab:/usr/sbin/nologin' > "$fixture/after-reboot-account.txt"
printf '%s\n' 999 > "$fixture/after-reboot-main-pid-owner.txt"
printf '%s\n' '5.7.0+ds2-3build1' > "$fixture/after-reboot-podman-version.txt"
printf '%s\n' '4.3.6' > "$fixture/after-reboot-rabbitmq-version.txt"
printf '%s\n' "'rabbit@provision-lab-rabbitmq'" > "$fixture/after-reboot-node.txt"
printf '%s\n' 'provision-lab-rabbitmq.service' > "$fixture/after-reboot-unit-name.txt"
printf '%s\n' before > "$fixture/before-reboot-boot-id.txt"
printf '%s\n' after > "$fixture/after-reboot-boot-id.txt"
printf '%s\n' unit-definition > "$fixture/after-reboot-unit-definition.txt"
printf '%s\n' package-inventory > "$fixture/packages-after-reboot.txt"
printf '%s\n' probe > "$fixture/probe.sha256"
printf '%s\n' entrypoint > "$fixture/credential-entrypoint.sha256"

common=(
  --evidence-dir "$fixture"
  --approval-record "$fixture/approval-record.json"
  --environment lab
  --operator marlinf
  --image "$image"
  --expected-podman-version 5.7.0+ds2-3build1
  --unit provision-lab-rabbitmq.service
  --container provision-lab-rabbitmq
  --queue provision-issue36
)

"$validator" "${common[@]}" > "$work_dir/result.json"
python3 - "$work_dir/result.json" "$image" "$config" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    result = json.load(handle)

assert result["result"] == "passed"
assert result["runtime"]["image"] == sys.argv[2]
assert result["runtime"]["imageConfig"] == sys.argv[3]
assert result["service"]["identity"] == "provision-lab"
assert result["service"]["unit"] == "provision-lab-rabbitmq.service"
assert result["service"]["observedMainPidUid"] == 999
assert result["topology"] == {
    "brokerNodes": 1,
    "hostFailureTolerance": 0,
    "queue": "provision-issue36",
    "queueDurable": True,
    "queueMembers": 1,
    "queueType": "quorum",
}
contract = result["queueContract"]
assert contract["delivery"]["value"] == "at-least-once"
assert contract["delivery"]["evidence"] == "stable-ID redelivery after unacknowledged channel close"
assert contract["publisherConfirm"]["status"] == "qualified"
assert contract["consumerAcknowledgement"]["status"] == "qualified"
for name in ("retry", "deadLetter", "retention", "ordering", "deduplication"):
    assert contract[name]["status"] == "unqualified"
assert result["observations"]["alarms"]["status"] == "observed"
assert result["observations"]["features"]["status"] == "observed"
assert result["observations"]["health"]["value"] == "healthy"
PY

cp "$fixture/after-reboot-container.json" "$work_dir/container-good.json"
sed 's/34fc91a9/04fc91a9/' "$work_dir/container-good.json" > "$fixture/after-reboot-container.json"
if "$validator" "${common[@]}" >/dev/null 2>&1; then
  fail "a container running the wrong platform digest was accepted"
fi
cp "$work_dir/container-good.json" "$fixture/after-reboot-container.json"

cp "$fixture/after-reboot-queues.json" "$work_dir/queues-good.json"
sed 's/"durable":true/"durable":false/' "$work_dir/queues-good.json" > "$fixture/after-reboot-queues.json"
if "$validator" "${common[@]}" >/dev/null 2>&1; then
  fail "a non-durable Queue was accepted"
fi
cp "$work_dir/queues-good.json" "$fixture/after-reboot-queues.json"

printf '%s\n' '{"queue":"provision-issue36","members":["rabbit@one","rabbit@two"]}' > "$fixture/after-reboot-quorum-members.json"
if "$validator" "${common[@]}" >/dev/null 2>&1; then
  fail "the wrong quorum member count was accepted"
fi
printf '%s\n' '{"queue":"provision-issue36","members":["rabbit@provision-lab-rabbitmq"]}' > "$fixture/after-reboot-quorum-members.json"

printf '%s\n' 1000 > "$fixture/after-reboot-main-pid-owner.txt"
if "$validator" "${common[@]}" >/dev/null 2>&1; then
  fail "a mismatched service process identity was accepted"
fi
printf '%s\n' 999 > "$fixture/after-reboot-main-pid-owner.txt"

sed 's/"redelivered":true/"redelivered":false/' \
  "$fixture/before-reboot-redelivery.json" > "$work_dir/redelivery-false.json"
cp "$work_dir/redelivery-false.json" "$fixture/before-reboot-redelivery.json"
if "$validator" "${common[@]}" >/dev/null 2>&1; then
  fail "at-least-once delivery was qualified without observed redelivery"
fi

printf 'rabbitmq qualification validator tests passed\n'
