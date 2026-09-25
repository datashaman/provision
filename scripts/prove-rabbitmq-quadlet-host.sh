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
  --qualification-validator FILE \
  --expected-podman-version VERSION --confirm-disposable-host HOSTNAME \
  --approval-record FILE --operator NAME \
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
qualification_validator=""
expected_podman_version=""
confirmed_host=""
approval_record=""
operator=""
evidence_dir="/var/lib/provision/evidence/issue36"
while (($#)); do
  case "$1" in
    --preview|--prepare|--verify-after-reboot) [[ -z "$mode" ]] || usage; mode="$1"; shift ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --oci-image) (($# >= 2)) || usage; oci_image="$2"; shift 2 ;;
    --probe-binary) (($# >= 2)) || usage; probe_binary="$2"; shift 2 ;;
    --qualification-validator) (($# >= 2)) || usage; qualification_validator="$2"; shift 2 ;;
    --expected-podman-version) (($# >= 2)) || usage; expected_podman_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --approval-record) (($# >= 2)) || usage; approval_record="$2"; shift 2 ;;
    --operator) (($# >= 2)) || usage; operator="$2"; shift 2 ;;
    --evidence-dir) (($# >= 2)) || usage; evidence_dir="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$mode" && "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ ]] || usage
[[ "$oci_image" =~ ^[a-z0-9.-]+([:/][a-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]] || \
  die "the OCI image must be a fully qualified digest reference"
[[ "$expected_podman_version" =~ ^[A-Za-z0-9.+:~_-]+$ ]] || usage
[[ "$confirmed_host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || usage
[[ "$operator" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || usage
[[ -f "$approval_record" && ! -L "$approval_record" ]] || \
  die "a regular, non-symlink approval record is required"
[[ -f "$qualification_validator" && ! -L "$qualification_validator" ]] || \
  die "a regular, non-symlink qualification validator is required"
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
installed_validator="/usr/local/libexec/provision-rabbitmq-qualification-validator"
credential_entrypoint="$service_root/credential-entrypoint"
credential_name="rabbitmq-config"

approval_source="$(python3 - "$approval_record" "$environment" "$operator" "$oci_image" <<'PY'
import json
import sys

path, environment, operator, image = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    approval = json.load(handle)

expected = {
    "schemaVersion": "provision.dev/rabbitmq-packaging-approval/v1alpha1",
    "issue": 36,
    "decision": "digest-pinned-rootless-oci-quadlet",
    "environment": environment,
    "operator": operator,
    "product": {"name": "RabbitMQ", "version": "4.3.6"},
    "image": image,
    "topology": {
        "brokerNodes": 1,
        "queueType": "quorum",
        "queueMembers": 1,
        "hostFailureTolerance": 0,
    },
}
for key, value in expected.items():
    if approval.get(key) != value:
        raise SystemExit(f"approval record does not bind expected {key}")
source = approval.get("source")
if not isinstance(source, dict) or not isinstance(source.get("kind"), str) or not source["kind"]:
    raise SystemExit("approval record lacks source kind")
if not isinstance(source.get("context"), str) or not source["context"]:
    raise SystemExit("approval record lacks source context")
print(source["kind"])
PY
)" || die "approval record validation failed"

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
  printf '  Approval: %s via %s\n' "$operator" "$approval_source"
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
  local prefix="$1" main_pid
  as_account systemctl --user show "$unit" \
    -p ActiveState -p SubState -p FragmentPath -p LoadCredential -p MainPID > "$evidence_dir/$prefix-unit.txt"
  as_account systemctl --user show "$unit" -p Id --value > "$evidence_dir/$prefix-unit-name.txt"
  main_pid="$(as_account systemctl --user show "$unit" -p MainPID --value)"
  stat -c %u "/proc/$main_pid" > "$evidence_dir/$prefix-main-pid-owner.txt"
  getent passwd "$account" > "$evidence_dir/$prefix-account.txt"
  as_account systemctl --user cat "$unit" > "$evidence_dir/$prefix-unit-definition.txt"
  as_account podman image inspect "$oci_image" > "$evidence_dir/$prefix-image.json"
  as_account podman inspect "$container" > "$evidence_dir/$prefix-container.json"
  dpkg-query -W -f='${Version}\n' podman > "$evidence_dir/$prefix-podman-version.txt"
  as_account podman exec "$container" rabbitmqctl version > "$evidence_dir/$prefix-rabbitmq-version.txt"
  as_account podman exec "$container" rabbitmqctl eval 'node().' > "$evidence_dir/$prefix-node.txt"
  as_account podman exec "$container" rabbitmqctl list_queues -q name type durable messages --formatter json > "$evidence_dir/$prefix-queues.json"
  as_account podman exec "$container" rabbitmq-queues quorum_status --vhost / "$queue" > "$evidence_dir/$prefix-quorum-status.txt"
  as_account podman exec "$container" rabbitmqctl cluster_status --formatter json > "$evidence_dir/$prefix-cluster.json"
  as_account podman exec "$container" rabbitmqctl list_feature_flags name state --formatter json > "$evidence_dir/$prefix-features.json"
  if as_account podman exec "$container" rabbitmq-diagnostics -q check_local_alarms >/dev/null; then
    printf '{"localAlarmCheckPassed":true}\n' > "$evidence_dir/$prefix-alarms.json"
  else
    printf '{"localAlarmCheckPassed":false}\n' > "$evidence_dir/$prefix-alarms.json"
  fi
  if as_account podman exec "$container" rabbitmq-diagnostics -q ping >/dev/null; then
    printf '{"diagnosticsPingPassed":true}\n' > "$evidence_dir/$prefix-ping.json"
  else
    printf '{"diagnosticsPingPassed":false}\n' > "$evidence_dir/$prefix-ping.json"
  fi
  python3 - "$evidence_dir/$prefix-quorum-status.txt" "$queue" "$evidence_dir/$prefix-quorum-members.json" <<'PY'
import json
import re
import sys

source, queue, destination = sys.argv[1:]
with open(source, encoding="utf-8") as handle:
    text = handle.read()
text = re.sub(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\([0-9A-Za-z])", "", text)
members = []
for line in text.splitlines():
    if "leader" not in line and "follower" not in line:
        continue
    members.extend(re.findall(r"rabbit@[A-Za-z0-9._-]+", line))
members = sorted(set(members))
if not members:
    raise SystemExit("quorum status contained no machine-readable member identity")
with open(destination, "w", encoding="utf-8") as handle:
    json.dump({"queue": queue, "members": members}, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY
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
  approved_manifest="${oci_image##*@}"
  observed_manifest="$(as_account podman inspect --format '{{.ImageDigest}}' "$container")"
  [[ "$observed_manifest" == "$approved_manifest" ]] || \
    die "the running container does not use the approved platform manifest"
  observed_config="$(as_account podman inspect --format '{{.Image}}' "$container")"
  image_config="$(as_account podman image inspect --format '{{.Id}}' "$oci_image")"
  [[ "${observed_config#sha256:}" == "${image_config#sha256:}" ]] || \
    die "the running container does not use the observed image configuration"
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
  main_pid="$(as_account systemctl --user show "$unit" -p MainPID --value)"
  [[ "$main_pid" =~ ^[1-9][0-9]*$ && -d "/proc/$main_pid" ]] || die "$unit has no live main process"
  [[ "$(stat -c %u "/proc/$main_pid")" == "$uid" ]] || \
    die "$unit main process does not run as the Environment account"
}

if [[ "$mode" == --prepare ]]; then
  [[ -f "$probe_binary" && ! -L "$probe_binary" ]] || die "probe binary is missing or is a symlink"
  [[ ! -e /srv/gimme && ! -L /srv/gimme ]] || die "Gimme-owned /srv/gimme exists"
  dpkg-query -W rabbitmq-server >/dev/null 2>&1 && die "native RabbitMQ is already installed"
  command -v podman >/dev/null 2>&1 && die "Podman is already installed; restore the clean snapshot first"
  getent passwd "$account" >/dev/null && die "$account already exists; restore the clean snapshot first"

  install -d -o root -g root -m 0755 "$evidence_dir"
  install -o root -g root -m 0644 "$approval_record" "$evidence_dir/approval-record.json"
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
  install -o root -g root -m 0755 "$qualification_validator" "$installed_validator"
  as_account podman pull "$oci_image"
  as_account podman image inspect "$oci_image" >/dev/null
  as_account systemctl --user daemon-reload
  as_account systemctl --user start "$unit"
  wait_for_service
  verify_exact_inventory

  run_probe roundtrip issue36-before-reboot-roundtrip "$evidence_dir/before-reboot-roundtrip.json"
  run_probe redelivery issue36-redelivery "$evidence_dir/before-reboot-redelivery.json"
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
cmp -s "$approval_record" "$evidence_dir/approval-record.json" || \
  die "post-reboot approval record differs from the approved prepare input"
[[ -x "$installed_probe" ]] || die "the installed acceptance probe is absent"
[[ -x "$installed_validator" ]] || die "the installed qualification validator is absent"
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

"$installed_validator" \
  --evidence-dir "$evidence_dir" \
  --approval-record "$evidence_dir/approval-record.json" \
  --environment "$environment" \
  --operator "$operator" \
  --image "$oci_image" \
  --expected-podman-version "$expected_podman_version" \
  --unit "$unit" \
  --container "$container" \
  --queue "$queue" > "$evidence_dir/support-observation.json"

printf 'RabbitMQ rootless Quadlet proof passed\n'
printf 'evidence: %s\n' "$evidence_dir"
