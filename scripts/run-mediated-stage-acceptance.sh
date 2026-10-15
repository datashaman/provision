#!/usr/bin/env bash
# Creates a disposable controller clone and restores the existing disposable Host VM.
# shellcheck disable=SC2029 # All expanded remote paths and instance names are fixed by this disposable exercise.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
base=${PROVISION_ACCEPTANCE_BASE:-base.local}
target=${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}
controller=provision-mediation-controller
build="$root/work/issue-130-live"
evidence="$build/evidence"
ssh_options=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2)
mkdir -p "$build"
[[ ! -e "$evidence" ]] || { echo "archive $evidence before rerunning" >&2; exit 2; }
GOOS=linux GOARCH=amd64 go build -o "$build/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build/provision-host-executor" ./cmd/provision-host-executor
source_version="$(git -C "$root" rev-parse HEAD)-tree-$(git -C "$root" write-tree)"
reset_host() {
  ssh "${ssh_options[@]}" "$base" bash -s <<'RESET'
set -euo pipefail
instance=provision-acceptance
incus info "$instance" >/dev/null
incus snapshot list "$instance" --format csv -c n | grep -Fxq clean
if [[ "$(incus list "$instance" --format csv -c s)" != STOPPED ]]; then incus stop "$instance" --timeout 60 || incus stop "$instance" --force; fi
incus snapshot restore "$instance" clean
incus start "$instance"
for _ in $(seq 1 90); do
  if incus exec -n "$instance" -- true >/dev/null 2>&1; then exit 0; fi
  sleep 2
done
exit 1
RESET
  for _ in {1..60}; do
    if ssh "${ssh_options[@]}" "marlinf@$target" true >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  echo "disposable Host did not become ready" >&2
  return 1
}
reset_host
# Do not delete a pre-existing machine. Failed runs are retained for diagnosis and explicit reset.
ssh "${ssh_options[@]}" "$base" bash -s -- "$controller" <<'REMOTE'
set -euo pipefail
controller=$1
if incus info "$controller" >/dev/null 2>&1; then echo "controller already exists; inspect and delete the prior disposable run explicitly" >&2; exit 2; fi
incus copy provision-acceptance/clean "$controller"
incus config set "$controller" user.provision-acceptance issue130
incus config set "$controller" limits.memory 2GiB
incus config set "$controller" limits.cpu 2
incus start "$controller"
for _ in $(seq 1 60); do
  if incus exec -n "$controller" -- true >/dev/null 2>&1; then break; fi
  sleep 2
done
incus exec -n "$controller" -- hostnamectl set-hostname "$controller"
incus exec -T "$controller" -- bash -s <<'SETUP'
set -euo pipefail
for account in authority requestbot requestother; do useradd -m -U -s /bin/bash "$account"; chmod 0700 "/home/$account"; done
usermod -aG requestbot,requestother authority
install -d -m 0755 /srv/mediation /srv/mediation/inbox
install -d -m 0755 /srv/mediation/results
install -d -o authority -g authority -m 0700 /srv/mediation/private /home/authority/.ssh
for account in requestbot requestother; do
 install -d -o authority -g "$account" -m 2730 "/srv/mediation/inbox/$account"
 install -d -o authority -g "$account" -m 2750 "/srv/mediation/results/$account"
done
runuser -u authority -- ssh-keygen -q -t ed25519 -N '' -f /home/authority/.ssh/id_ed25519
install -d -m 0700 /root/.ssh
ssh-keygen -q -t ed25519 -N '' -f /root/.ssh/id_ed25519
install -d -m 0755 /opt/provision/scripts/remote-ssh-fault-bin /opt/provision/testdata/direct-local-matrix
SETUP
REMOTE
controller_ip=$(ssh "${ssh_options[@]}" "$base" incus list "$controller" --format csv -c 4 | sed -E 's/ .*//')
[[ "$controller_ip" =~ ^[0-9.]+$ ]] || { echo "no controller IP" >&2; exit 1; }
# Transfer only the binaries and fixtures needed by this exercise.
transfer="/tmp/provision-issue130-$(date -u +%Y%m%dT%H%M%SZ)"
ssh "${ssh_options[@]}" "$base" mkdir -m 0700 "$transfer"
tar -czf "$build/controller-inputs.tar.gz" -C "$root" scripts/test-remote-ssh-host.sh scripts/test-mediated-stage-host.py scripts/remote-ssh-fault-bin/ssh testdata/direct-local-matrix/root.yaml testdata/direct-local-matrix/application.yaml testdata/direct-local-matrix/environment-remote.yaml.tmpl
scp -q "$build/provision" "$build/provision-host-executor" "$build/controller-inputs.tar.gz" scripts/bootstrap-host.sh "$base:$transfer/"
ssh "${ssh_options[@]}" "$base" bash -s -- "$controller" "$transfer" "$controller_ip" <<'REMOTE'
set -euo pipefail
controller=$1; transfer=$2; controller_ip=$3
incus file push -q "$transfer/provision" "$controller/usr/local/bin/provision" --mode 0755
incus file push -q "$transfer/controller-inputs.tar.gz" "$controller/tmp/controller-inputs.tar.gz"
incus exec -n "$controller" -- tar -xzf /tmp/controller-inputs.tar.gz -C /opt/provision
incus exec -n "$controller" -- runuser -u authority -- /usr/local/bin/provision authority keygen --private-key /srv/mediation/private/authority.key --public-key /srv/mediation/private/authority.pub
incus file pull -q "$controller/srv/mediation/private/authority.pub" "$transfer/authority.pub"
incus file pull -q "$controller/home/authority/.ssh/id_ed25519.pub" "$transfer/operator-ssh.pub"
incus file pull -q "$controller/root/.ssh/id_ed25519.pub" "$transfer/supervisor-ssh.pub"
incus exec -n "$controller" -- openssl req -x509 -newkey rsa:2048 -nodes -keyout /srv/mediation/private/origin.key -out /srv/mediation/private/origin.crt -days 1 -subj /CN=provision-mediation-origin -addext "subjectAltName=IP:$controller_ip" 2>/dev/null
incus file pull -q "$controller/srv/mediation/private/origin.crt" "$transfer/origin.crt"
REMOTE
# Administrative setup uses the existing trusted path; runtime operator has only executor sudo.
ssh "${ssh_options[@]}" "$base" incus file push -q "$transfer/provision-host-executor" provision-acceptance/tmp/provision-host-executor --mode 0755
ssh "${ssh_options[@]}" "$base" incus file push -q "$transfer/bootstrap-host.sh" provision-acceptance/tmp/bootstrap-host.sh
for name in authority.pub operator-ssh.pub supervisor-ssh.pub origin.crt; do
 ssh "${ssh_options[@]}" "$base" incus file push -q "$transfer/$name" "provision-acceptance/tmp/$name"
done
ssh "${ssh_options[@]}" "$base" incus exec -T provision-acceptance -- bash -s <<'REMOTE'
set -euo pipefail
useradd -m -U -s /bin/bash mediation-host
usermod -p '*' mediation-host
install -d -o mediation-host -g mediation-host -m 0700 /home/mediation-host/.ssh
install -o mediation-host -g mediation-host -m 0600 /tmp/operator-ssh.pub /home/mediation-host/.ssh/authorized_keys
cat /tmp/supervisor-ssh.pub >> /home/marlinf/.ssh/authorized_keys
install -m 0644 /tmp/origin.crt /usr/local/share/ca-certificates/provision-mediation-origin.crt
update-ca-certificates >/dev/null
bash /tmp/bootstrap-host.sh --apply --replace-executor --environment lab --operator mediation-host --binary /tmp/provision-host-executor --authority-public-key /tmp/authority.pub
REMOTE
# Pin the Host's public SSH key through the administrative Incus path, not TOFU.
ssh "${ssh_options[@]}" "$base" incus exec -n provision-acceptance -- cat /etc/ssh/ssh_host_ed25519_key.pub >"$build/host-ssh.pub"
read -r key_type key_value _ <"$build/host-ssh.pub"
printf '%s %s %s\n' "$target" "$key_type" "$key_value" >"$build/known_hosts"
scp -q "$build/known_hosts" "$base:$transfer/known_hosts"
ssh "${ssh_options[@]}" "$base" bash -s -- "$controller" "$transfer" <<'REMOTE'
set -euo pipefail
controller=$1; transfer=$2
incus file push -q "$transfer/known_hosts" "$controller/tmp/known_hosts"
incus exec -n "$controller" -- install -o authority -g authority -m 0600 /tmp/known_hosts /home/authority/.ssh/known_hosts
incus exec -n "$controller" -- install -m 0600 /tmp/known_hosts /root/.ssh/known_hosts
REMOTE
# The existing remote-SSH entry point dispatches the isolated-account matrix.
set +e
ssh "${ssh_options[@]}" "$base" incus exec -n "$controller" -- bash /opt/provision/scripts/test-remote-ssh-host.sh --mediated --target "$target" --target-user mediation-host --controller-address "$controller_ip" --provision-version "$source_version" --work-dir /srv/mediation/evidence --confirm-disposable-host provision-acceptance
status=$?
set -e
ssh "${ssh_options[@]}" "$base" incus exec -n "$controller" -- tar -czf /tmp/evidence.tar.gz -C /srv/mediation evidence
ssh "${ssh_options[@]}" "$base" incus file pull -q "$controller/tmp/evidence.tar.gz" "$transfer/evidence.tar.gz"
scp -q "$base:$transfer/evidence.tar.gz" "$build/evidence.tar.gz"
tar -xzf "$build/evidence.tar.gz" -C "$build"
[[ "$status" == 0 ]] || { echo "acceptance failed; disposable VMs retained for diagnosis" >&2; exit "$status"; }
reset_host
ssh "${ssh_options[@]}" "$base" incus stop "$controller" --force
ssh "${ssh_options[@]}" "$base" incus delete "$controller"
echo "Mediated stageArtifact acceptance passed; evidence: $evidence"
