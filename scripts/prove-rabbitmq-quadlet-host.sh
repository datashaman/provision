#!/usr/bin/env bash
# Destructive, issue-36-only acceptance proof for an explicitly disposable
# Ubuntu Host Target. This is not a production Queue executor operation.

set -euo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

usage() {
  cat >&2 <<'EOF'
usage: prove-rabbitmq-quadlet-host.sh (--preview|--prepare|--verify-after-reboot) \
  --environment NAME --oci-image NAME@sha256:DIGEST --probe-binary FILE \
  --expected-podman-version VERSION --confirm-disposable-host HOSTNAME \
  [--evidence-dir DIRECTORY]
EOF
  exit 2
}

die() {
  printf 'prove-rabbitmq-quadlet-host: %s\n' "$*" >&2
  exit 1
}

mode=""
environment=""
oci_image=""
probe_binary=""
expected_podman_version=""
confirmed_host=""
evidence_dir="/var/lib/provision/evidence/issue36"
while (($#)); do
  case "$1" in
    --preview|--prepare|--verify-after-reboot) [[ -z "$mode" ]] || usage; mode="$1"; shift ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --oci-image) (($# >= 2)) || usage; oci_image="$2"; shift 2 ;;
    --probe-binary) (($# >= 2)) || usage; probe_binary="$2"; shift 2 ;;
    --expected-podman-version) (($# >= 2)) || usage; expected_podman_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --evidence-dir) (($# >= 2)) || usage; evidence_dir="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$mode" && "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ ]] || usage
[[ "$oci_image" =~ ^[a-z0-9.-]+([:/][a-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]] || \
  die "the OCI image must be a fully qualified digest reference"
[[ "$expected_podman_version" =~ ^[A-Za-z0-9.+:~_-]+$ ]] || usage
[[ "$confirmed_host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || usage
[[ "$evidence_dir" == /* && "$evidence_dir" =~ ^/var/lib/provision/evidence/[a-zA-Z0-9._/-]+$ ]] || \
  die "the evidence directory must be below /var/lib/provision/evidence"

account="provision-$environment"
container="provision-$environment-rabbitmq"
unit="$container.service"
queue="provision-issue36"
amqp_port=25672
home="/var/lib/provision/runtime/$environment"
service_root="/var/lib/provision/environments/$environment/services/rabbitmq"
data_dir="$service_root/data"
installed_probe="/usr/local/libexec/provision-rabbitmq-acceptance-probe"
credential_entrypoint="$service_root/credential-entrypoint"
credential_name="rabbitmq-config"

if [[ "$mode" == --preview ]]; then
  printf 'RabbitMQ rootless Quadlet proof preview:\n'
  printf '  Environment: %s\n' "$environment"
  printf '  Service identity: %s (rootless)\n' "$account"
  printf '  OCI image: %s\n' "$oci_image"
  printf '  Podman package: %s\n' "$expected_podman_version"
  printf '  Unit: %s\n' "$unit"
  printf '  Queue: %s (quorum, one member, zero Host-failure tolerance)\n' "$queue"
  printf '  Durable state: %s\n' "$data_dir"
  printf '  Credentials: encrypted user-scoped systemd credential %s\n' "$credential_name"
  printf '  Evidence: %s\n' "$evidence_dir"
  printf 'No changes made.\n'
  exit 0
fi

[[ "$EUID" -eq 0 ]] || die "live proof phases require root"
[[ "$(hostname -s)" == "$confirmed_host" ]] || \
  die "refusing Host $(hostname -s); confirmation names $confirmed_host"
[[ "$(uname -s)" == Linux ]] || die "live proof requires Linux"
# shellcheck source=/dev/null
. /etc/os-release
[[ "${ID:-}" == ubuntu && "${VERSION_ID:-}" == 26.04 ]] || \
  die "live proof requires the qualified Ubuntu 26.04 image"

as_account() {
  (
    cd "$home"
    runuser -u "$account" -- env \
      HOME="$home" \
      XDG_RUNTIME_DIR="/run/user/$uid" \
      DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
      "$@"
  )
}

record_packages() {
  dpkg-query -W -f='${binary:Package}=${Version}\n' | LC_ALL=C sort > "$1"
}

wait_for_service() {
  local state="" health=""
  for _ in $(seq 1 120); do
    state="$(as_account systemctl --user is-active "$unit" 2>/dev/null || true)"
    health="$(as_account podman inspect --format '{{.State.Health.Status}}' "$container" 2>/dev/null || true)"
    if [[ "$state" == active && "$health" == healthy ]]; then
      return 0
    fi
    sleep 1
  done
  as_account systemctl --user status "$unit" --no-pager >&2 || true
  as_account journalctl --user -u "$unit" -n 100 --no-pager >&2 || true
  die "RabbitMQ did not become active and healthy (service=$state, health=$health)"
}

run_probe() {
  local proof_mode="$1" message_id="$2" destination="$3" transient_unit
  transient_unit="provision-rabbitmq-proof-${proof_mode//-/_}-$(date +%s%N)"
  as_account systemd-run --user --quiet --wait --pipe --collect \
    --unit="$transient_unit" \
    --property="LoadCredentialEncrypted=$credential_name" \
    --setenv="PROVISION_RABBITMQ_PORT=$amqp_port" \
    --setenv="PROVISION_RABBITMQ_QUEUE=$queue" \
    --setenv="PROVISION_RABBITMQ_MESSAGE_ID=$message_id" \
    --setenv="PROVISION_RABBITMQ_MODE=$proof_mode" \
    "$installed_probe" > "$destination"
}

record_observation() {
  local prefix="$1"
  as_account systemctl --user show "$unit" \
    -p ActiveState -p SubState -p FragmentPath -p LoadCredential -p MainPID > "$evidence_dir/$prefix-unit.txt"
  as_account systemctl --user cat "$unit" > "$evidence_dir/$prefix-unit-definition.txt"
  as_account podman image inspect "$oci_image" > "$evidence_dir/$prefix-image.json"
  as_account podman inspect "$container" > "$evidence_dir/$prefix-container.json"
  as_account podman exec "$container" rabbitmqctl version > "$evidence_dir/$prefix-rabbitmq-version.txt"
  as_account podman exec "$container" rabbitmqctl eval 'node().' > "$evidence_dir/$prefix-node.txt"
  as_account podman exec "$container" rabbitmqctl list_queues -q name type durable messages --formatter json > "$evidence_dir/$prefix-queues.json"
  as_account podman exec "$container" rabbitmq-queues quorum_status --vhost / "$queue" > "$evidence_dir/$prefix-quorum-status.txt"
  as_account podman ps --format json > "$evidence_dir/$prefix-containers.json"
  as_account podman images --format json > "$evidence_dir/$prefix-images.json"
  find "/etc/containers/systemd/users/$uid" -maxdepth 1 -type f -printf '%M %u:%g %p\n' | LC_ALL=C sort > "$evidence_dir/$prefix-quadlet-files.txt"
  find "$service_root" -maxdepth 2 -printf '%M %u:%g %p\n' | LC_ALL=C sort > "$evidence_dir/$prefix-service-paths.txt"
  ss -H -ltn "sport = :$amqp_port" > "$evidence_dir/$prefix-listener.txt"
}

verify_exact_inventory() {
  [[ "$(as_account podman ps -a --format '{{.Names}}' | wc -l)" -eq 1 ]] || \
    die "the Environment account has an unexpected container set"
  [[ "$(as_account podman ps -a --format '{{.Names}}')" == "$container" ]] || \
    die "the Environment account has an unexpected container identity"
  [[ "$(as_account podman images --format '{{.Repository}}@{{.Digest}}' | wc -l)" -eq 1 ]] || \
    die "the Environment account has an unexpected image set"
  as_account podman image exists "$oci_image" || die "the approved OCI digest is absent"
  [[ "$(find "/etc/containers/systemd/users/$uid" -maxdepth 1 -type f -printf '%f\n')" == "$container.container" ]] || \
    die "the root-owned Quadlet inventory is not exact"
  dpkg-query -W rabbitmq-server >/dev/null 2>&1 && die "native RabbitMQ was unexpectedly installed"
  as_account podman exec "$container" test -r /run/provision-runtime/rabbitmq.conf || \
    die "the encrypted systemd credential is not available to RabbitMQ through its tmpfs runtime copy"
  [[ "$(as_account podman inspect --format '{{range .Mounts}}{{if eq .Destination "/run/provision-credential/rabbitmq.conf"}}{{.RW}}{{end}}{{end}}' "$container")" == false ]] || \
    die "the RabbitMQ credential mount is writable"
  as_account podman inspect --format '{{json .HostConfig.Tmpfs}}' "$container" | \
    grep -Fq '"/run/provision-runtime"' || die "the RabbitMQ runtime credential copy is not backed by tmpfs"
  if grep -R -l '^default_pass[[:space:]]*=' "$service_root" >/dev/null 2>&1; then
    die "plaintext RabbitMQ credentials were written to durable service storage"
  fi
  [[ "$(as_account systemctl --user is-active "$unit")" == active ]] || die "$unit is not active"
}

if [[ "$mode" == --prepare ]]; then
  [[ -f "$probe_binary" && ! -L "$probe_binary" ]] || die "probe binary is missing or is a symlink"
  [[ ! -e /srv/gimme && ! -L /srv/gimme ]] || die "Gimme-owned /srv/gimme exists"
  dpkg-query -W rabbitmq-server >/dev/null 2>&1 && die "native RabbitMQ is already installed"
  command -v podman >/dev/null 2>&1 && die "Podman is already installed; restore the clean snapshot first"
  getent passwd "$account" >/dev/null && die "$account already exists; restore the clean snapshot first"

  install -d -o root -g root -m 0755 "$evidence_dir"
  record_packages "$evidence_dir/packages-before.txt"
  cat /proc/sys/kernel/random/boot_id > "$evidence_dir/before-reboot-boot-id.txt"

  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y "podman=$expected_podman_version"
  [[ "$(dpkg-query -W -f='${Version}' podman)" == "$expected_podman_version" ]] || \
    die "installed Podman version differs from the approved version"

  install -d -o root -g root -m 0755 /var/lib/provision /var/lib/provision/runtime \
    /var/lib/provision/environments "/var/lib/provision/environments/$environment" \
    "/var/lib/provision/environments/$environment/services" "$service_root"
  useradd --system --user-group --home-dir "$home" --create-home --shell /usr/sbin/nologin "$account"
  uid="$(id -u "$account")"
  gid="$(id -g "$account")"
  usermod --add-subuids 165536-231071 --add-subgids 165536-231071 "$account"
  install -d -o "$uid" -g "$gid" -m 0700 "$data_dir" "$home/.config" "$home/.config/credstore.encrypted"
  install -d -o root -g root -m 0755 "/etc/containers/systemd/users/$uid"
  loginctl enable-linger "$account"
  systemctl start "user@$uid.service"
  for _ in $(seq 1 30); do
    [[ -S "/run/user/$uid/bus" ]] && break
    sleep 1
  done
  [[ -S "/run/user/$uid/bus" ]] || die "the lingering user manager did not start"

  systemd-creds setup >/dev/null
  credential_tmp="$(mktemp)"
  trap 'rm -f "${credential_tmp:-}"' EXIT
  password="$(openssl rand -hex 24)"
  {
    printf 'listeners.tcp.default = 5672\n'
    printf 'default_user = provision-issue36\n'
    printf 'default_pass = %s\n' "$password"
    printf 'default_queue_type = quorum\n'
  } | systemd-creds encrypt --uid="$uid" --name="$credential_name" - "$credential_tmp" >/dev/null
  password=""
  install -o "$uid" -g "$gid" -m 0600 "$credential_tmp" "$home/.config/credstore.encrypted/$credential_name"
  rm -f "$credential_tmp"
  trap - EXIT

  entrypoint_tmp="$(mktemp)"
  trap 'rm -f "${entrypoint_tmp:-}"' EXIT
  cat > "$entrypoint_tmp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
install -d -o rabbitmq -g rabbitmq -m 0700 /run/provision-runtime
install -o rabbitmq -g rabbitmq -m 0400 \
  /run/provision-credential/rabbitmq.conf \
  /run/provision-runtime/rabbitmq.conf
export RABBITMQ_CONFIG_FILE=/run/provision-runtime/rabbitmq.conf
exec /usr/local/bin/docker-entrypoint.sh "$@"
EOF
  install -o root -g root -m 0755 "$entrypoint_tmp" "$credential_entrypoint"
  rm -f "$entrypoint_tmp"
  trap - EXIT

  quadlet_tmp="$(mktemp)"
  trap 'rm -f "${quadlet_tmp:-}"' EXIT
  cat > "$quadlet_tmp" <<EOF
[Unit]
Description=Provision managed RabbitMQ for Environment $environment
Wants=network-online.target
After=network-online.target

[Container]
Image=$oci_image
ContainerName=$container
PublishPort=127.0.0.1:$amqp_port:5672
Volume=$data_dir:/var/lib/rabbitmq
Volume=%d/$credential_name:/run/provision-credential/rabbitmq.conf:ro
Volume=$credential_entrypoint:/usr/local/bin/provision-rabbitmq-credential-entrypoint:ro
Tmpfs=/run/provision-runtime:rw,mode=0755
Entrypoint=/usr/local/bin/provision-rabbitmq-credential-entrypoint
Exec=rabbitmq-server
Environment=RABBITMQ_NODENAME=rabbit@$container
HealthCmd=rabbitmq-diagnostics -q ping
HealthInterval=5s
HealthRetries=24
NoNewPrivileges=true

[Service]
LoadCredentialEncrypted=$credential_name
Restart=always
TimeoutStartSec=900

[Install]
WantedBy=default.target
EOF
  install -o root -g root -m 0644 "$quadlet_tmp" "/etc/containers/systemd/users/$uid/$container.container"
  rm -f "$quadlet_tmp"
  trap - EXIT

  install -o root -g root -m 0755 "$probe_binary" "$installed_probe"
  as_account podman pull "$oci_image"
  as_account podman image inspect "$oci_image" >/dev/null
  as_account systemctl --user daemon-reload
  as_account systemctl --user start "$unit"
  wait_for_service
  verify_exact_inventory

  run_probe roundtrip issue36-before-reboot-roundtrip "$evidence_dir/before-reboot-roundtrip.json"
  run_probe publish-only issue36-reboot-survival "$evidence_dir/before-reboot-persistent.json"
  record_observation before-reboot
  record_packages "$evidence_dir/packages-after.txt"
  diff -u "$evidence_dir/packages-before.txt" "$evidence_dir/packages-after.txt" > "$evidence_dir/package-diff.txt" || true
  sha256sum "$installed_probe" > "$evidence_dir/probe.sha256"
  sha256sum "$credential_entrypoint" > "$evidence_dir/credential-entrypoint.sha256"
  sha256sum "$home/.config/credstore.encrypted/$credential_name" > "$evidence_dir/encrypted-credential.sha256"
  printf 'prepare proof passed; reboot the Host, then run --verify-after-reboot\n'
  exit 0
fi

getent passwd "$account" >/dev/null || die "$account is absent; run --prepare first"
uid="$(id -u "$account")"
gid="$(id -g "$account")"
[[ -f "$evidence_dir/before-reboot-boot-id.txt" ]] || die "pre-reboot evidence is absent"
[[ -x "$installed_probe" ]] || die "the installed acceptance probe is absent"
before_boot="$(cat "$evidence_dir/before-reboot-boot-id.txt")"
after_boot="$(cat /proc/sys/kernel/random/boot_id)"
[[ "$before_boot" != "$after_boot" ]] || die "the Host has not rebooted since prepare"
printf '%s\n' "$after_boot" > "$evidence_dir/after-reboot-boot-id.txt"
wait_for_service
verify_exact_inventory
run_probe consume-existing issue36-reboot-survival "$evidence_dir/after-reboot-consume.json"
record_observation after-reboot
record_packages "$evidence_dir/packages-after-reboot.txt"
cmp -s "$evidence_dir/packages-after.txt" "$evidence_dir/packages-after-reboot.txt" || \
  die "the installed package inventory changed across reboot"

python3 - "$evidence_dir" "$environment" "$account" "$uid" "$oci_image" "$expected_podman_version" "$unit" "$container" "$queue" <<'PY'
import hashlib
import json
import os
import platform
import sys

root, environment, account, uid, image, podman_version, unit, container, queue = sys.argv[1:]

def read(name):
    with open(os.path.join(root, name), encoding="utf-8") as handle:
        return handle.read().strip()

def read_json(name):
    with open(os.path.join(root, name), encoding="utf-8") as handle:
        return json.load(handle)

def digest(name):
    value = hashlib.sha256()
    with open(os.path.join(root, name), "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            value.update(chunk)
    return "sha256:" + value.hexdigest()

roundtrip = read_json("before-reboot-roundtrip.json")
persistent = read_json("before-reboot-persistent.json")
consumed = read_json("after-reboot-consume.json")
queue_observation = read_json("after-reboot-queues.json")
matching_queue = [item for item in queue_observation if item.get("name") == queue]
if len(matching_queue) != 1 or matching_queue[0].get("type") != "quorum":
    raise SystemExit("the final Queue topology is not the expected quorum Queue")

result = {
    "schemaVersion": "provision.dev/rabbitmq-packaging-qualification/v1alpha1",
    "result": "passed",
    "decision": "digest-pinned-rootless-oci-quadlet",
    "environment": environment,
    "host": {
        "os": "ubuntu",
        "osVersion": "26.04",
        "architecture": platform.machine(),
        "kernel": platform.release(),
        "systemdVersion": os.popen("systemctl --version").read().splitlines()[0],
        "bootIdChanged": read("before-reboot-boot-id.txt") != read("after-reboot-boot-id.txt"),
    },
    "runtime": {
        "podmanVersion": podman_version,
        "image": image,
        "rabbitmqVersion": read("after-reboot-rabbitmq-version.txt"),
        "node": read("after-reboot-node.txt"),
    },
    "service": {
        "identity": account,
        "uid": uid,
        "unit": unit,
        "container": container,
        "rootless": True,
        "activeAfterReboot": True,
        "credential": "encrypted-user-scoped-systemd-credential",
        "credentialCipherDigest": read("encrypted-credential.sha256").split()[0],
        "plaintextRecorded": False,
    },
    "topology": {
        "brokerNodes": 1,
        "queue": queue,
        "queueType": "quorum",
        "queueMembers": 1,
        "hostFailureTolerance": 0,
    },
    "capabilities": {
        "publisherConfirm": roundtrip["publisherConfirmed"] and persistent["publisherConfirmed"],
        "consumerAcknowledgement": roundtrip["consumerAcknowledged"] and consumed["consumerAcknowledged"],
        "stableMessageIdentityAcrossReboot": persistent["messageId"] == consumed["messageId"],
        "persistentMessageSurvivedReboot": persistent["messagesAfterOperation"] == 1 and consumed["messagesAfterOperation"] == 0,
        "serviceSurvivedReboot": True,
        "hostLossTolerance": False,
    },
    "evidenceDigests": {
        "packageInventory": digest("packages-after-reboot.txt"),
        "unitDefinition": digest("after-reboot-unit-definition.txt"),
        "imageObservation": digest("after-reboot-image.json"),
        "containerObservation": digest("after-reboot-container.json"),
        "queueObservation": digest("after-reboot-queues.json"),
        "quorumStatus": digest("after-reboot-quorum-status.txt"),
        "probeBinary": read("probe.sha256").split()[0],
        "credentialEntrypoint": read("credential-entrypoint.sha256").split()[0],
    },
}

if not all((
    result["host"]["bootIdChanged"],
    result["capabilities"]["publisherConfirm"],
    result["capabilities"]["consumerAcknowledgement"],
    result["capabilities"]["stableMessageIdentityAcrossReboot"],
    result["capabilities"]["persistentMessageSurvivedReboot"],
)):
    raise SystemExit("one or more qualification assertions failed")

with open(os.path.join(root, "support-observation.json"), "w", encoding="utf-8") as handle:
    json.dump(result, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY

printf 'RabbitMQ rootless Quadlet proof passed\n'
printf 'evidence: %s\n' "$evidence_dir"
