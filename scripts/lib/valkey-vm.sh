# Shared disposable-VM plumbing for the direct-local Valkey runners. Callers define root, target,
# target_user, authority, secret, remote, remote_evidence, local_evidence, build_dir, ssh_opts and scp_opts.
# shellcheck disable=SC2029,SC2154 # remote commands expand client-side paths; variables come from the caller

cleanup_needed=false
cleanup() {
  local status=$?
  if [[ "$cleanup_needed" == true && "$status" -ne 0 ]]; then
    mkdir -p "$local_evidence.failed"
    scp "${scp_opts[@]}" -r "$target_user@$target:$remote_evidence" "$local_evidence.failed/remote" >/dev/null 2>&1 || true
    [[ -n "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]] || "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
  fi
  exit "$status"
}

# vm_prepare restores the clean snapshot, installs the built binaries and scripts, and bootstraps the Host.
vm_prepare() {
  cleanup_needed=true
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
  # The restored snapshot is old, so apt's catch-up timers fire after boot and starve the 2-vCPU VM for minutes,
  # long enough to expire a 30s execution lease mid-operation. Stop them for this boot.
  ssh "${ssh_opts[@]}" "$target_user@$target" "sudo -n systemctl stop apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service"
  ssh "${ssh_opts[@]}" "$target_user@$target" "rm -rf '$remote' '$remote_evidence'; mkdir -p '$remote' '$remote_evidence' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" "$build_dir/provision" "$build_dir/provision-host-executor" "$authority/host-authority.pub" "$authority/host-authority.key" "$secret" "$target_user@$target:$remote/"
  scp "${scp_opts[@]}" -r "$root/scripts" "$root/examples" "$target_user@$target:$remote/"
  ssh "${ssh_opts[@]}" "$target_user@$target" "chmod 0600 '$remote/host-authority.key' '$remote/$(basename "$secret")'"
  # The package install can restart VM networking and drop this session; bootstrap is idempotent, so retry.
  local attempt
  for attempt in 1 2 3; do
    ssh "${ssh_opts[@]}" "$target_user@$target" \
      "sudo -n bash '$remote/scripts/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'" && break
    [[ $attempt -lt 3 ]] || { echo "bootstrap failed after $attempt attempts" >&2; exit 1; }
    until ssh "${ssh_opts[@]}" "$target_user@$target" true 2>/dev/null; do sleep 5; done
  done
}
