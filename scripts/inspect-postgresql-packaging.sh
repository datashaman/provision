#!/usr/bin/env bash
# Read-only PostgreSQL packaging comparison probe. This does not install,
# pull, create accounts, start services, or enable managed operations.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: inspect-postgresql-packaging.sh --target local|USER@HOST --environment NAME \
  --oci-image NAME@sha256:DIGEST
EOF
  exit 2
}

die() {
  printf 'inspect-postgresql-packaging: %s\n' "$*" >&2
  exit 1
}

target=""
environment=""
oci_image=""
while (($#)); do
  case "$1" in
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --oci-image) (($# >= 2)) || usage; oci_image="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ "$target" == local || "$target" =~ ^[a-z_][a-z0-9_-]*@[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || \
  die "--target must be local or a simple USER@HOST SSH destination"
[[ "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ ]] || usage
[[ "$oci_image" =~ ^[a-z0-9.-]+([:/][a-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]] || \
  die "--oci-image must be a fully qualified digest reference; tags are not accepted"

remote_script='
set -euo pipefail
environment="$1"
oci_image="$2"
account="provision-$environment"
json_string() {
  python3 -c "import json,sys; print(json.dumps(sys.stdin.read().strip()))"
}
field() {
  local name="$1"
  local value="$2"
  printf "  \"%s\": %s" "$name" "$(printf "%s" "$value" | json_string)"
}
candidate() {
  apt-cache policy "$1" 2>/dev/null | awk "/Candidate:/ {print \$2; exit}"
}
installed() {
  dpkg-query -W -f="\${Version}" "$1" 2>/dev/null || true
}
printf "{\n"
field schemaVersion "provision.dev/postgresql-packaging-inspection/v1alpha1"; printf ",\n"
field decisionStatus "awaiting-human-approval"; printf ",\n"
printf "  \"mutatingOperationsEnabled\": false,\n"
field environment "$environment"; printf ",\n"
field host "$(hostname -f 2>/dev/null || hostname)"; printf ",\n"
field kernel "$(uname -r)"; printf ",\n"
field architecture "$(uname -m)"; printf ",\n"
. /etc/os-release
field os "${ID:-}"; printf ",\n"
field osVersion "${VERSION_ID:-}"; printf ",\n"
field systemd "$(systemctl --version 2>/dev/null | sed -n 1p)"; printf ",\n"
field cgroupV2 "$([[ -f /sys/fs/cgroup/cgroup.controllers ]] && echo true || echo false)"; printf ",\n"
field nativePostgreSQLInstalled "$(installed postgresql)"; printf ",\n"
field nativePostgreSQLCandidate "$(candidate postgresql)"; printf ",\n"
field nativePostgreSQL18Candidate "$(candidate postgresql-18)"; printf ",\n"
field podmanInstalled "$(installed podman)"; printf ",\n"
field podmanCandidate "$(candidate podman)"; printf ",\n"
field environmentAccount "$(getent passwd "$account" || true)"; printf ",\n"
field selectedManagedImage "$oci_image"; printf ",\n"
printf "  \"selectedManagedPath\": \"digest-pinned-rootless-oci-quadlet\",\n"
printf "  \"hostInstalledPostgreSQLManaged\": false\n"
printf "}\n"
'

if [[ "$target" == local ]]; then
  bash -s -- "$environment" "$oci_image" <<< "$remote_script"
else
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 \
    "$target" bash -s -- "$environment" "$oci_image" <<< "$remote_script"
fi
