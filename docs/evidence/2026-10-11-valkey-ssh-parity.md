# Valkey Store Transition SSH parity — 2026-10-11

Live proof for #101. `scripts/run-remote-ssh-valkey-transition-matrix.sh` runs the direct-local
failure matrix from [#100](2026-10-11-valkey-direct-local-failure-matrix.md), then the identical
matrix over pinned strict SSH from the controller (`scripts/test-direct-local-valkey-transition-host.sh
--target`), each mode from the clean `provision-acceptance` snapshot, then
`scripts/valkey-transition-parity.py` compares the pairs. Source: clean commit `8157e76`
(no `-dirty-` suffix). Structured record: [JSON](2026-10-11-valkey-ssh-parity.json), which holds both the direct-local and SSH accounting per mode. Raw per-run evidence stays in the git-ignored `work/`; snapshot identity is not recorded, only that the VM was restored.

Same configuration, Plans, approvals, interruption points and assertions in both transports: it is the
same script. Over SSH the controller holds the State Backend, signing key and approvals; the Host sees
only strict-SSH observation and signed restricted-executor operations. The SSH fault wrapper kills the
controller before the executor runs (`before`) or after it completed and returned (`after`), around
every one of the six transition operations. In both cases `resume` observes the remote Host first:
`before` executes once, `after` records the completed effect and the executor is not invoked again
(`mutationReplayedOnResume: false` throughout). The fault is controller death at the SSH call, a proxy for transport loss, not a severed TCP connection. `undecidable` makes the authority-switch evidence
unreadable; resume records `uncertain` with reason and recovery action and does not restart the
authoritative container.

## Parity

For `before`, `after` and `undecidable`, the comparison asserts: transports differ, both passed, kernel,
OS, architecture and systemd match, executor digest matches, and the normalized accounting is identical
(logical store, generation identities, workload keys before and after, write fence, authority switch,
retention, per-operation outcomes, fault and uncertain records, owned-resource inventory). Replication
offsets at the fence differ between runs, as in #100, so only offset equality and link state are
compared. Host: Ubuntu 26.04, Linux 7.0.0-34, systemd 259, Valkey `9.1.1` → `9.1.2`, controller
Darwin arm64. The VM was restored before every mode and verified clean after the last.

## Harness defect found by parity

An earlier SSH run read back one workload key instead of two: `ssh` consumed the stdin of the
`while read` key loop. The parity comparison caught it; `host_run` now uses `ssh -n` and the script
fails unless every declared key is read back. All evidence above is from after that fix.

## Intermittent failure at the authority switch (open)

Across the attempts for this issue, three runs failed at `switchKeyValueAuthority` and passed on
retry from the same source: SSH `before` and direct-local `before` ("candidate offset to equal the
active master_repl_offset over an up link"), and direct-local `after` (resume re-invoked the
executor for the switch). The direct-local failures show it is not SSH-specific. The cause is not
established; it is plausibly executor `podman exec` observations stalling on the VM within the 5s
pre-switch window, but that is unverified. The offset-check error now reports the last observed
replication state so the next occurrence is diagnosable. This parity record is a passing run; it
does not claim the switch step is flake-free.
