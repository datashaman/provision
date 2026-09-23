#!/usr/bin/env bash
# One-time Ubuntu host preparation for the native HTTP tracer. Run only on an
# explicitly supplied, disposable Host Target after its ownership is clear.
set -euo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

usage() {
  echo "usage: $0 (--dry-run|--apply) --environment NAME --operator USER --binary LINUX_EXECUTOR" >&2
  exit 2
}

mode=""
environment=""
operator=""
binary=""
while (($#)); do
  case "$1" in
    --dry-run|--apply) [[ -z "$mode" ]] || usage; mode="$1"; shift ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --operator) (($# >= 2)) || usage; operator="$2"; shift 2 ;;
    --binary) (($# >= 2)) || usage; binary="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$mode" && "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ && "$operator" =~ ^[a-z_][a-z0-9_-]*$ && "$operator" != root && -f "$binary" && ! -L "$binary" ]] || usage

account="provision-$environment"
executor_path="/usr/local/libexec/provision-host-executor"
record_path="/etc/provision/bootstrap/$environment.json"
sudoers_path="/etc/sudoers.d/provision-$environment"
environment_home="/var/lib/provision/environments/$environment"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f1
  else
    shasum -a 256 "$1" | cut -d ' ' -f1
  fi
}

digest="sha256:$(sha256_file "$binary")"
if [[ "$mode" == --dry-run ]]; then
  printf 'Host bootstrap preview only:\n'
  printf '  Environment: %s\n  Account: %s (nologin)\n  Operator: %s\n' "$environment" "$account" "$operator"
  printf '  Executor: %s (%s)\n' "$executor_path" "$digest"
  printf '  Record: %s\n  Restricted sudoers: %s\n' "$record_path" "$sudoers_path"
  printf '  Required services: systemd, Caddy\n'
  printf '  Deployment operations remain disabled until Plan-bound authorization exists.\nNo changes made.\n'
  exit 0
fi

[[ "$(id -u)" == 0 ]] || { echo "apply requires root" >&2; exit 1; }
[[ "$(uname -s)" == Linux ]] || { echo "apply requires Linux" >&2; exit 1; }
grep -Eq '^ID="?ubuntu"?$' /etc/os-release || { echo "apply currently supports Ubuntu only" >&2; exit 1; }
[[ ! -e /srv/gimme && ! -L /srv/gimme ]] || { echo "Gimme-owned /srv/gimme exists; refusing bootstrap" >&2; exit 1; }
if find /etc/systemd/system -maxdepth 1 -name 'gimme-*' -print -quit | grep -q .; then
  echo "Gimme-managed systemd units exist; refusing bootstrap" >&2
  exit 1
fi
id -u "$operator" >/dev/null 2>&1 || { echo "operator user does not exist" >&2; exit 1; }
for path in /usr/local/libexec /etc/provision /etc/provision/bootstrap /var/lib/provision /var/lib/provision/environments; do
  [[ ! -L "$path" ]] || { echo "symlink at $path; refusing bootstrap" >&2; exit 1; }
done
for path in "$executor_path" "$record_path" "$sudoers_path" "$environment_home"; do
  [[ ! -L "$path" ]] || { echo "symlink at $path; refusing bootstrap" >&2; exit 1; }
done
if [[ -e "$executor_path" && "sha256:$(sha256_file "$executor_path")" != "$digest" ]]; then
  echo "an existing executor has different bytes; refusing bootstrap" >&2
  exit 1
fi
if getent passwd "$account" >/dev/null; then
  entry="$(getent passwd "$account")"
  IFS=: read -r _ _ existing_uid _ _ existing_home existing_shell <<< "$entry"
  [[ "$existing_uid" =~ ^[0-9]+$ && "$existing_uid" -gt 0 && "$existing_uid" -lt 1000 && "$existing_home" == "$environment_home" && "$existing_shell" == /usr/sbin/nologin ]] || {
    echo "Environment account differs from requested identity" >&2; exit 1;
  }
fi

record_tmp="$(mktemp)"
sudoers_tmp="$(mktemp)"
trap 'rm -f "$record_tmp" "$sudoers_tmp"' EXIT
printf '{"schemaVersion":"provision.dev/bootstrap/v1","environment":"%s","operator":"%s","account":"%s","executorDigest":"%s"}\n' \
  "$environment" "$operator" "$account" "$digest" > "$record_tmp"
printf '%s ALL=(root) NOPASSWD: %s\n' "$operator" "$executor_path" > "$sudoers_tmp"
visudo -cf "$sudoers_tmp" >/dev/null
if [[ -e "$record_path" ]] && ! cmp -s "$record_tmp" "$record_path"; then
  echo "bootstrap record differs; refusing to overwrite" >&2
  exit 1
fi
if [[ -e "$sudoers_path" ]] && ! cmp -s "$sudoers_tmp" "$sudoers_path"; then
  echo "executor sudoers rule differs; refusing to overwrite" >&2
  exit 1
fi

if ! command -v caddy >/dev/null 2>&1; then
  DEBIAN_FRONTEND=noninteractive apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
fi
systemctl enable --now caddy

if ! getent passwd "$account" >/dev/null; then
  adduser --system --group --no-create-home --home "$environment_home" --shell /usr/sbin/nologin "$account"
fi
install -d -o root -g root -m 0755 /usr/local/libexec /etc/provision /etc/provision/bootstrap /var/lib/provision /var/lib/provision/environments
install -d -o "$account" -g "$account" -m 0750 "$environment_home"
if [[ ! -e "$executor_path" ]]; then
  install -o root -g root -m 0755 "$binary" "$executor_path"
fi
install -o root -g root -m 0644 "$record_tmp" "$record_path"
install -o root -g root -m 0440 "$sudoers_tmp" "$sudoers_path"
visudo -cf "$sudoers_path" >/dev/null
sudo -n -u "$operator" -- sudo -n "$executor_path" inspect --environment "$environment" --operator "$operator"
