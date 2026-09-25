#!/usr/bin/env bash
# One-time Ubuntu host preparation for the native HTTP tracer. Run only on an
# explicitly supplied, disposable Host Target after its ownership is clear.
set -euo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

usage() {
  echo "usage: $0 (--dry-run|--apply) --environment NAME --operator USER --binary LINUX_EXECUTOR --authority-public-key FILE" >&2
  exit 2
}

mode=""
environment=""
operator=""
binary=""
authority_public_key=""
while (($#)); do
  case "$1" in
    --dry-run|--apply) [[ -z "$mode" ]] || usage; mode="$1"; shift ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --operator) (($# >= 2)) || usage; operator="$2"; shift 2 ;;
    --binary) (($# >= 2)) || usage; binary="$2"; shift 2 ;;
    --authority-public-key) (($# >= 2)) || usage; authority_public_key="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$mode" && "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ && "$operator" =~ ^[a-z_][a-z0-9_-]*$ && "$operator" != root && -f "$binary" && ! -L "$binary" && -f "$authority_public_key" && ! -L "$authority_public_key" ]] || usage

account="provision-$environment"
executor_path="/usr/local/libexec/provision-host-executor"
record_path="/etc/provision/bootstrap/$environment.json"
sudoers_path="/etc/sudoers.d/provision-$environment"
environment_home="/var/lib/provision/environments/$environment"
release_root="$environment_home/releases"
authority_path="/etc/provision/authority/$environment.pub"
authority_state="/var/lib/provision/authority/$environment"
artifact_cache="/var/lib/provision/artifacts/sha256"
caddy_override_dir="/etc/systemd/system/caddy.service.d"
caddy_override_path="$caddy_override_dir/provision.conf"
caddy_autosave_path="/var/lib/caddy/.config/caddy/autosave.json"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f1
  else
    shasum -a 256 "$1" | cut -d ' ' -f1
  fi
}

sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d ' ' -f1
  else
    shasum -a 256 | cut -d ' ' -f1
  fi
}

digest="sha256:$(sha256_file "$binary")"
authority_key="$(tr -d '\n' < "$authority_public_key")"
[[ "$authority_key" =~ ^[0-9a-f]{64}$ ]] || { echo "authority public key is invalid" >&2; exit 1; }
authority_key_id="sha256:$(printf '%s' "$authority_key" | sha256_stdin)"
if [[ "$mode" == --dry-run ]]; then
  printf 'Host bootstrap preview only:\n'
  printf '  Environment: %s\n  Account: %s (nologin)\n  Operator: %s\n' "$environment" "$account" "$operator"
  printf '  Executor: %s (%s)\n' "$executor_path" "$digest"
  printf '  Authorization verifier: %s (%s)\n' "$authority_path" "$authority_key_id"
  printf '  Record: %s\n  Restricted sudoers: %s\n' "$record_path" "$sudoers_path"
  printf '  Durable Caddy service override: %s\n' "$caddy_override_path"
  printf '  Required services: systemd, Caddy\n'
  printf '  Enabled mutations: signed, Plan-bound Artifact staging, candidate install/start/verification, verified Endpoint switch, and stable health verification with bounded rollback.\nNo changes made.\n'
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
for path in /usr/local/libexec /etc/provision /etc/provision/bootstrap /etc/provision/authority /var/lib/provision /var/lib/provision/environments /var/lib/provision/authority /var/lib/provision/artifacts "$artifact_cache" "$authority_state" "$caddy_override_dir"; do
  [[ ! -L "$path" ]] || { echo "symlink at $path; refusing bootstrap" >&2; exit 1; }
done
for path in "$executor_path" "$record_path" "$sudoers_path" "$environment_home" "$release_root" "$authority_path" "$caddy_override_path"; do
  [[ ! -L "$path" ]] || { echo "symlink at $path; refusing bootstrap" >&2; exit 1; }
done
expect_existing_path() {
  local path="$1" kind="$2" expected="$3" actual
  [[ -e "$path" ]] || return 0
  if [[ "$kind" == directory ]]; then
    [[ -d "$path" ]] || { echo "expected directory at $path; refusing bootstrap" >&2; exit 1; }
  else
    [[ -f "$path" ]] || { echo "expected regular file at $path; refusing bootstrap" >&2; exit 1; }
  fi
  actual="$(stat -c '%u:%g:%a' "$path")"
  [[ "$actual" == "$expected" ]] || { echo "ownership or mode drift at $path ($actual, expected $expected); refusing bootstrap" >&2; exit 1; }
}
for path in /usr/local/libexec /etc/provision /etc/provision/bootstrap /etc/provision/authority /var/lib/provision /var/lib/provision/environments /var/lib/provision/artifacts "$artifact_cache"; do
  expect_existing_path "$path" directory 0:0:755
done
for path in /var/lib/provision/authority "$authority_state"; do
  expect_existing_path "$path" directory 0:0:700
done
expect_existing_path "$executor_path" file 0:0:755
expect_existing_path "$record_path" file 0:0:644
expect_existing_path "$sudoers_path" file 0:0:440
expect_existing_path "$authority_path" file 0:0:644
expect_existing_path "$caddy_override_dir" directory 0:0:755
expect_existing_path "$caddy_override_path" file 0:0:644
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
  expect_existing_path "$environment_home" directory 0:0:755
elif [[ -e "$environment_home" ]]; then
  echo "Environment home exists without its account; refusing bootstrap" >&2
  exit 1
fi
expect_existing_path "$release_root" directory 0:0:755

record_tmp="$(mktemp)"
sudoers_tmp="$(mktemp)"
authority_tmp="$(mktemp)"
caddy_override_tmp="$(mktemp)"
trap 'rm -f "$record_tmp" "$sudoers_tmp" "$authority_tmp" "$caddy_override_tmp"' EXIT
printf '%s\n' "$authority_key" > "$authority_tmp"
printf '{"schemaVersion":"provision.dev/bootstrap/v2","environment":"%s","operator":"%s","account":"%s","executorDigest":"%s","authorityKeyId":"%s"}\n' \
  "$environment" "$operator" "$account" "$digest" "$authority_key_id" > "$record_tmp"
printf '%s ALL=(root) NOPASSWD: %s\n' "$operator" "$executor_path" > "$sudoers_tmp"
printf '%s\n' \
  '[Service]' \
  'ExecStart=' \
  'ExecStart=/usr/bin/caddy run --environ --resume' \
  'ExecReload=' \
  "ExecReload=/usr/bin/caddy reload --config $caddy_autosave_path --force" > "$caddy_override_tmp"
visudo -cf "$sudoers_tmp" >/dev/null
if [[ -e "$record_path" ]] && ! cmp -s "$record_tmp" "$record_path"; then
  echo "bootstrap record differs; refusing to overwrite" >&2
  exit 1
fi
if [[ -e "$sudoers_path" ]] && ! cmp -s "$sudoers_tmp" "$sudoers_path"; then
  echo "executor sudoers rule differs; refusing to overwrite" >&2
  exit 1
fi
if [[ -e "$authority_path" ]] && ! cmp -s "$authority_tmp" "$authority_path"; then
  echo "authorization public key differs; refusing to overwrite" >&2
  exit 1
fi
if [[ -e "$caddy_override_path" ]] && ! cmp -s "$caddy_override_tmp" "$caddy_override_path"; then
  echo "Caddy service override differs; refusing to overwrite" >&2
  exit 1
fi

if ! command -v caddy >/dev/null 2>&1; then
  DEBIAN_FRONTEND=noninteractive apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
fi
systemctl enable --now caddy
for _ in {1..50}; do
  [[ -f "$caddy_autosave_path" ]] && break
  sleep 0.1
done
[[ -f "$caddy_autosave_path" ]] || { echo "Caddy autosaved configuration is unavailable; refusing bootstrap" >&2; exit 1; }
if [[ ! -e "$caddy_override_path" ]]; then
  install -d -o root -g root -m 0755 "$caddy_override_dir"
  install -o root -g root -m 0644 "$caddy_override_tmp" "$caddy_override_path"
  systemctl daemon-reload
  systemctl restart caddy
fi

if ! getent passwd "$account" >/dev/null; then
  adduser --system --group --no-create-home --home "$environment_home" --shell /usr/sbin/nologin "$account"
fi
install -d -o root -g root -m 0755 /usr/local/libexec /etc/provision /etc/provision/bootstrap /etc/provision/authority /var/lib/provision /var/lib/provision/environments /var/lib/provision/artifacts "$artifact_cache"
install -d -o root -g root -m 0700 /var/lib/provision/authority "$authority_state"
install -d -o root -g root -m 0755 "$environment_home"
install -d -o root -g root -m 0755 "$release_root"
if [[ ! -e "$executor_path" ]]; then
  install -o root -g root -m 0755 "$binary" "$executor_path"
fi
install -o root -g root -m 0644 "$record_tmp" "$record_path"
install -o root -g root -m 0644 "$authority_tmp" "$authority_path"
install -o root -g root -m 0440 "$sudoers_tmp" "$sudoers_path"
visudo -cf "$sudoers_path" >/dev/null
inspection="$(sudo -n -u "$operator" -- sudo -n "$executor_path" inspect --environment "$environment" --operator "$operator")"
printf '%s\n' "$inspection"
if ! grep -Eq '"ready":[[:space:]]*true' <<< "$inspection"; then
  echo "bootstrap inspection reported not ready; see findings above" >&2
  exit 1
fi
