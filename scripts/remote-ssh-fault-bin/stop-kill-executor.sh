#!/usr/bin/env bash
set -euo pipefail

operator="${1:-}"
[[ "$operator" =~ ^[a-z_][a-z0-9_-]*$ ]] || exit 2

for ((attempt = 0; attempt < 2000; attempt++)); do
  for process in /proc/[0-9]*; do
    [[ "$(readlink -f "$process/exe" 2>/dev/null || true)" == /usr/local/libexec/provision-host-executor ]] || continue
    arguments="$(tr '\0' ' ' <"$process/cmdline" 2>/dev/null || true)"
    [[ " $arguments " == *" execute "* ]] || continue
    pid="${process##*/}"
    kill -STOP "$pid"
    install -o "$operator" -g "$operator" -m 0600 /dev/null /tmp/provision-acceptance-executor-stopped
    sleep 5
    kill -KILL "$pid" 2>/dev/null || true
    exit 0
  done
  sleep 0.01
done
exit 1
