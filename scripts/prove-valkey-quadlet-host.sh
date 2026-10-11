#!/usr/bin/env bash
# Destructive, issue-95-only acceptance proof for an explicitly disposable
# Ubuntu Host Target. This selects packaging only; it does not enable a
# production key-value store lifecycle operation.

set -euo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

usage() {
  cat >&2 <<'EOF'
usage: prove-valkey-quadlet-host.sh (--preview|--prepare|--verify-after-reboot) \
  --environment NAME --oci-image NAME@sha256:DIGEST \
  --expected-podman-version VERSION --confirm-disposable-host HOSTNAME \
  --approval-record FILE --operator NAME [--evidence-dir DIRECTORY] \
  [--valkey-version VERSION --image-index sha256:DIGEST]
EOF
  exit 2
}

die() {
  printf 'prove-valkey-quadlet-host: %s\n' "$*" >&2
  exit 1
}

mode=""
environment=""
oci_image=""
expected_podman_version=""
confirmed_host=""
approval_record=""
operator=""
evidence_dir="/var/lib/provision/evidence/issue95"
valkey_version="9.1.2"
image_index="sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd"
while (($#)); do
  case "$1" in
    --preview|--prepare|--verify-after-reboot) [[ -z "$mode" ]] || usage; mode="$1"; shift ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --oci-image) (($# >= 2)) || usage; oci_image="$2"; shift 2 ;;
    --expected-podman-version) (($# >= 2)) || usage; expected_podman_version="$2"; shift 2 ;;
    --confirm-disposable-host) (($# >= 2)) || usage; confirmed_host="$2"; shift 2 ;;
    --approval-record) (($# >= 2)) || usage; approval_record="$2"; shift 2 ;;
    --operator) (($# >= 2)) || usage; operator="$2"; shift 2 ;;
    --evidence-dir) (($# >= 2)) || usage; evidence_dir="$2"; shift 2 ;;
    --valkey-version) (($# >= 2)) || usage; valkey_version="$2"; shift 2 ;;
    --image-index) (($# >= 2)) || usage; image_index="$2"; shift 2 ;;
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
[[ "$evidence_dir" == /* && "$evidence_dir" =~ ^/var/lib/provision/evidence/[a-zA-Z0-9._/-]+$ ]] || \
  die "the evidence directory must be below /var/lib/provision/evidence"

[[ "$valkey_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ &&"$image_index" =~ ^sha256:[0-9a-f]{64}$ ]] || usage
# The image is digest-pinned, so the platform manifest is the reference digest.
image_manifest="${oci_image##*@}"
manifest_hex="${image_manifest#sha256:}"
generation="valkey-${valkey_version//./-}-${manifest_hex:0:12}"
account="provision-$environment"
container="provision-$environment-valkey"
unit="$container.service"
username="provision_admin"
valkey_port=26379
runtime_home="/var/lib/provision/runtime/$environment"
service_root="/var/lib/provision/environments/$environment/services/valkey"
generation_root="$service_root/generations/$generation"
data_dir="$generation_root/data"
credential_name="valkey-credential"
credential_entrypoint="$service_root/credential-entrypoint"
quadlet_path=""

approval_source="$(python3 - "$approval_record" "$environment" "$operator" "$oci_image" "$valkey_version" "$image_index" "$image_manifest" <<'PY'
import json
import sys

path, environment, operator, image, version, index, manifest = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    approval = json.load(handle)

expected = {
    "schemaVersion": "provision.dev/valkey-packaging-approval/v1alpha1",
    "issue": 95,
    "decision": "digest-pinned-rootless-oci-quadlet",
    "environment": environment,
    "operator": operator,
    "product": {"name": "Valkey", "version": version},
    "image": image,
    "imageIndex": index,
    "imageManifest": manifest,
    "topology": {
        "keyValueNodes": 1,
        "hostFailureTolerance": 0,
        "managedGeneration": "one-physical-store-generation",
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
  printf 'Valkey rootless Quadlet proof preview:\n'
  printf '  Environment: %s\n' "$environment"
  printf '  Service identity: %s (rootless)\n' "$account"
  printf '  Product: Valkey %s\n' "$valkey_version"
  printf '  OCI image: %s\n' "$oci_image"
  printf '  Image index: %s\n' "$image_index"
  printf '  Platform manifest: %s\n' "$image_manifest"
  printf '  Podman package: %s\n' "$expected_podman_version"
  printf '  Unit: %s\n' "$unit"
  printf '  Data path: %s\n' "$data_dir"
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
    cd "$runtime_home"
    runuser -u "$account" -- env \
      HOME="$runtime_home" \
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
  die "Valkey did not become active and healthy (service=$state, health=$health)"
}

cli_at() {
  as_account podman exec "$container" /tmp/provision-cli.sh "$@"
}

record_observation() {
  local prefix="$1" main_pid
  as_account systemctl --user show "$unit" \
    -p ActiveState -p SubState -p FragmentPath -p LoadCredential -p MainPID > "$evidence_dir/$prefix-unit.txt"
  as_account systemctl --user cat "$unit" > "$evidence_dir/$prefix-unit-definition.txt"
  main_pid="$(as_account systemctl --user show "$unit" -p MainPID --value)"
  stat -c %u "/proc/$main_pid" > "$evidence_dir/$prefix-main-pid-owner.txt"
  getent passwd "$account" > "$evidence_dir/$prefix-account.txt"
  as_account podman image inspect "$oci_image" > "$evidence_dir/$prefix-image.json"
  as_account podman inspect "$container" > "$evidence_dir/$prefix-container.json"
  dpkg-query -W -f='${Version}\n' podman > "$evidence_dir/$prefix-podman-version.txt"
  cli_at INFO server | tr -d '\r' | grep -E '^valkey_version:' > "$evidence_dir/$prefix-valkey-version.txt"
  cli_at ACL WHOAMI > "$evidence_dir/$prefix-connectivity.txt"
  cli_at CONFIG GET appendonly | tr -d '\r' > "$evidence_dir/$prefix-appendonly.txt"
  cli_at CONFIG GET appendfsync | tr -d '\r' > "$evidence_dir/$prefix-appendfsync.txt"
  cli_at INFO persistence | tr -d '\r' | grep -E '^aof_enabled:' > "$evidence_dir/$prefix-aof-enabled.txt"
  cli_at --scan --pattern 'issue95:*' | tr -d '\r' | LC_ALL=C sort > "$evidence_dir/$prefix-data.txt"
  as_account podman ps --format json > "$evidence_dir/$prefix-containers.json"
  as_account podman images --format json > "$evidence_dir/$prefix-images.json"
  find "/etc/containers/systemd/users/$uid" -maxdepth 1 -type f -printf '%M %u:%g %p\n' | LC_ALL=C sort > "$evidence_dir/$prefix-quadlet-files.txt"
  find "$service_root" -maxdepth 4 -printf '%M %u:%g %p\n' | LC_ALL=C sort > "$evidence_dir/$prefix-service-paths.txt"
  ss -H -ltn "sport = :$valkey_port" > "$evidence_dir/$prefix-listener.txt"
}

verify_exact_inventory() {
  local actual_config binding credential_mount_rw expected_config observed_digest
  [[ "$(as_account podman ps -a --format '{{.Names}}' | wc -l)" -eq 1 ]] || \
    die "the Environment account has an unexpected container set"
  [[ "$(as_account podman ps -a --format '{{.Names}}')" == "$container" ]] || \
    die "the Environment account container is not $container"
  observed_digest="$(as_account podman inspect --format '{{.ImageDigest}}' "$container")"
  case "$observed_digest" in
    "$oci_image"|"$image_manifest"|"$image_index"|*"@$image_manifest"|*"@$image_index") ;;
    *) die "running Valkey container is not using an approved digest (observed=$observed_digest expectedManifest=$image_manifest expectedIndex=$image_index)" ;;
  esac
  actual_config="$(as_account podman inspect --format '{{.Image}}' "$container")"
  expected_config="$(as_account podman image inspect --format '{{.Id}}' "$oci_image")"
  [[ "$actual_config" == "$expected_config" ]] || \
    die "running Valkey image config differs from the approved image (observed=$actual_config expected=$expected_config)"
  binding="$(as_account podman port "$container" 6379/tcp)"
  [[ "$binding" == "127.0.0.1:$valkey_port" ]] || \
    die "Valkey is not bound only to the planned loopback port (observed=$binding expected=127.0.0.1:$valkey_port)"
  credential_mount_rw="$(as_account podman inspect --format '{{range .}}{{range .Mounts}}{{if eq .Destination "/run/provision-credential/valkey-credential"}}{{.RW}}{{end}}{{end}}{{end}}' "$container")"
  [[ "$credential_mount_rw" == false ]] || \
    die "Valkey credential mount is writable or absent (observedRW=$credential_mount_rw)"
  [[ "$(cli_at CONFIG GET appendonly | tr -d '\r' | sed -n 2p)" == yes && \
     "$(cli_at CONFIG GET appendfsync | tr -d '\r' | sed -n 2p)" == everysec ]] || \
    die "Valkey is not running with AOF appendonly yes and appendfsync everysec"
}

write_generation_record() {
  local durable="$1"
  python3 - "$generation_root/generation.json" "$environment" "$generation" "$valkey_version" "$image_index" "$image_manifest" "$oci_image" "$unit" "$container" "$account" "$data_dir" "$quadlet_path" "$durable" <<'PY'
import json
import sys

(
    destination,
    environment,
    generation,
    version,
    image_index,
    image_manifest,
    image,
    unit,
    container,
    account,
    data_path,
    quadlet_path,
    durable,
) = sys.argv[1:]
record = {
    "schemaVersion": "provision.dev/host-valkey-packaging-generation/v1alpha1",
    "logicalId": f"provision-{environment}-primary",
    "generationId": generation,
    "valkeyVersion": version,
    "imageIndex": image_index,
    "imageManifest": image_manifest,
    "imageReference": image,
    "serviceUnit": unit,
    "container": container,
    "account": account,
    "dataPath": data_path,
    "quadletPath": quadlet_path,
    "persistence": {"appendonly": "yes", "appendfsync": "everysec"},
    "credentialReference": f"secret://{environment}/valkey-credential",
    "connectivity": True,
    "durableRestart": durable == "true",
}
with open(destination, "w", encoding="utf-8") as handle:
    json.dump(record, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY
  chmod 0444 "$generation_root/generation.json"
}

write_support_observation() {
  python3 - "$evidence_dir/support-observation.json" "$environment" "$operator" "$oci_image" "$image_index" "$image_manifest" "$generation" "$unit" "$container" "$account" "$data_dir" "$quadlet_path" "$expected_podman_version" "$valkey_version" <<'PY'
import json
import sys

(
    destination,
    environment,
    operator,
    image,
    image_index,
    image_manifest,
    generation,
    unit,
    container,
    account,
    data_path,
    quadlet_path,
    podman,
    version,
) = sys.argv[1:]
record = {
    "schemaVersion": "provision.dev/valkey-packaging-qualification/v1alpha1",
    "issue": 95,
    "result": "passed",
    "environment": environment,
    "operator": operator,
    "product": {"name": "Valkey", "version": version},
    "packaging": "digest-pinned-rootless-oci-quadlet",
    "image": image,
    "imageIndex": image_index,
    "imageManifest": image_manifest,
    "podmanVersion": podman,
    "serviceUnit": unit,
    "container": container,
    "account": account,
    "generation": generation,
    "dataPath": data_path,
    "quadletPath": quadlet_path,
    "credentialBoundary": "encrypted-user-scoped-systemd-credential",
    "topology": {
        "keyValueNodes": 1,
        "hostFailureTolerance": 0,
        "managedGeneration": "one-physical-store-generation",
    },
    "qualified": [
        "service-start",
        "client-connectivity",
        "aof-everysec-persistence",
        "owned-generation-data-path",
        "non-secret-credential-reference",
        "durable-restart",
    ],
    "notQualified": [
        "production-key-value-store-operation",
        "store-transition",
        "backup",
        "restore",
        "host-loss-recovery",
        "automatic-store-rollback-after-candidate-writes",
    ],
}
with open(destination, "w", encoding="utf-8") as handle:
    json.dump(record, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY
}

if [[ "$mode" == --prepare ]]; then
  [[ ! -e "$evidence_dir" ]] || die "evidence directory already exists"
  install -d -o root -g root -m 0755 "$evidence_dir"
  install -o root -g root -m 0644 "$approval_record" "$evidence_dir/approval-record.json"
  record_packages "$evidence_dir/packages-before.txt"
  cat /proc/sys/kernel/random/boot_id > "$evidence_dir/before-reboot-boot-id.txt"

  export DEBIAN_FRONTEND=noninteractive
  if ! command -v podman >/dev/null 2>&1; then
    apt-get update
    apt-get install -y "podman=$expected_podman_version"
  fi
  [[ "$(dpkg-query -W -f='${Version}' podman)" == "$expected_podman_version" ]] || \
    die "installed Podman version differs from the approved version"

  install -d -o root -g root -m 0755 /var/lib/provision /var/lib/provision/runtime \
    /var/lib/provision/environments "/var/lib/provision/environments/$environment" \
    "/var/lib/provision/environments/$environment/services" "$service_root" "$generation_root"
  if ! getent passwd "$account" >/dev/null; then
    useradd --system --user-group --home-dir "$runtime_home" --create-home --shell /usr/sbin/nologin "$account"
  fi
  uid="$(id -u "$account")"
  gid="$(id -g "$account")"
  quadlet_path="/etc/containers/systemd/users/$uid/$container.container"
  grep -q "^$account:" /etc/subuid || usermod --add-subuids 165536-231071 "$account"
  grep -q "^$account:" /etc/subgid || usermod --add-subgids 165536-231071 "$account"
  install -d -o "$uid" -g "$gid" -m 0700 "$runtime_home" "$runtime_home/.config" "$runtime_home/.config/credstore.encrypted"
  install -d -o root -g root -m 0755 "/etc/containers/systemd/users/$uid"
  install -d -o "$uid" -g "$gid" -m 0700 "$data_dir"
  loginctl enable-linger "$account"
  systemctl start "user@$uid.service"
  for _ in $(seq 1 30); do
    [[ -S "/run/user/$uid/bus" ]] && break
    sleep 1
  done
  [[ -S "/run/user/$uid/bus" ]] || die "the lingering user manager did not start"

  systemd-creds setup >/dev/null
  credential_tmp="$(mktemp)"
  password="$(openssl rand -hex 24)"
  printf 'valkey://%s:%s@127.0.0.1:%s\n' "$username" "$password" "$valkey_port" | \
    systemd-creds encrypt --uid="$uid" --name="$credential_name" - "$credential_tmp" >/dev/null
  install -o "$uid" -g "$gid" -m 0600 "$credential_tmp" "$runtime_home/.config/credstore.encrypted/$credential_name"
  rm -f "$credential_tmp"

  entrypoint_tmp="$(mktemp)"
  cat > "$entrypoint_tmp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="$(cat /run/provision-credential/valkey-credential)"
case "$url" in
  valkey://*@127.0.0.1:*) ;;
  *) echo "unsupported credential URL" >&2; exit 1 ;;
esac
credential_pair="${url#valkey://}"
credential_pair="${credential_pair%%@*}"
user="${credential_pair%%:*}"
password="${credential_pair#*:}"
[[ -n "$user" && -n "$password" ]] || { echo "incomplete Valkey credential URL" >&2; exit 1; }
hash="$(printf '%s' "$password" | sha256sum | cut -d' ' -f1)"
printf 'user default off\nuser %s on #%s ~* &* +@all\n' "$user" "$hash" > /tmp/users.acl
chmod 0644 /tmp/users.acl
cat > /tmp/provision-cli.sh <<CLI
#!/bin/sh
REDISCLI_AUTH='$password' exec valkey-cli --user '$user' "\$@"
CLI
cat > /tmp/provision-health.sh <<HEALTH
#!/bin/sh
[ "\$(/tmp/provision-cli.sh ping 2>/dev/null)" = PONG ]
HEALTH
chmod 0700 /tmp/provision-cli.sh /tmp/provision-health.sh
exec docker-entrypoint.sh "$@"
EOF
  install -o root -g root -m 0755 "$entrypoint_tmp" "$credential_entrypoint"
  rm -f "$entrypoint_tmp"
  password=""

  quadlet_tmp="$(mktemp)"
  cat > "$quadlet_tmp" <<EOF
[Unit]
Description=Provision managed Valkey for Environment $environment
Wants=network-online.target
After=network-online.target

[Container]
Image=$oci_image
ContainerName=$container
PublishPort=127.0.0.1:$valkey_port:6379
Volume=$data_dir:/data
Volume=%d/$credential_name:/run/provision-credential/valkey-credential:ro
Volume=$credential_entrypoint:/usr/local/bin/provision-valkey-credential-entrypoint:ro
Entrypoint=/usr/local/bin/provision-valkey-credential-entrypoint
Exec=valkey-server --aclfile /tmp/users.acl --appendonly yes --appendfsync everysec --dir /data --bind 0.0.0.0
HealthCmd=/tmp/provision-health.sh
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
  install -o root -g root -m 0644 "$quadlet_tmp" "$quadlet_path"
  rm -f "$quadlet_tmp"

  as_account podman pull "$oci_image"
  as_account podman image inspect "$oci_image" >/dev/null
  as_account podman unshare chown -R 999:999 "$data_dir"
  as_account podman unshare chmod 0700 "$data_dir"
  as_account systemctl --user daemon-reload
  as_account systemctl --user start "$unit"
  wait_for_service
  verify_exact_inventory
  [[ "$(cli_at SET issue95:before-reboot 1)" == OK* ]] || die "Valkey rejected the pre-reboot probe write"
  write_generation_record false
  record_observation before-reboot
  record_packages "$evidence_dir/packages-after.txt"
  diff -u "$evidence_dir/packages-before.txt" "$evidence_dir/packages-after.txt" > "$evidence_dir/package-diff.txt" || true
  sha256sum "$credential_entrypoint" > "$evidence_dir/credential-entrypoint.sha256"
  sha256sum "$runtime_home/.config/credstore.encrypted/$credential_name" > "$evidence_dir/encrypted-credential.sha256"
  printf 'prepare proof passed; reboot the Host, then run --verify-after-reboot\n'
  exit 0
fi

getent passwd "$account" >/dev/null || die "$account is absent; run --prepare first"
uid="$(id -u "$account")"
gid="$(id -g "$account")"
quadlet_path="/etc/containers/systemd/users/$uid/$container.container"
[[ -f "$evidence_dir/before-reboot-boot-id.txt" ]] || die "pre-reboot evidence is absent"
cmp -s "$approval_record" "$evidence_dir/approval-record.json" || \
  die "post-reboot approval record differs from the approved prepare input"
before_boot="$(cat "$evidence_dir/before-reboot-boot-id.txt")"
after_boot="$(cat /proc/sys/kernel/random/boot_id)"
[[ "$before_boot" != "$after_boot" ]] || die "the Host has not rebooted since prepare"
printf '%s\n' "$after_boot" > "$evidence_dir/after-reboot-boot-id.txt"
wait_for_service
verify_exact_inventory
[[ "$(cli_at SET issue95:after-reboot 1)" == OK* ]] || die "Valkey rejected the post-reboot probe write"
[[ "$(cli_at EXISTS issue95:before-reboot issue95:after-reboot | tr -d '\r')" == 2 ]] || \
  die "Valkey durable data did not survive reboot"
write_generation_record true
record_observation after-reboot
record_packages "$evidence_dir/packages-after-reboot.txt"
cmp -s "$evidence_dir/packages-after.txt" "$evidence_dir/packages-after-reboot.txt" || \
  die "the installed package inventory changed across reboot"
write_support_observation
printf 'Valkey rootless Quadlet proof passed\n'
printf 'evidence: %s\n' "$evidence_dir"
