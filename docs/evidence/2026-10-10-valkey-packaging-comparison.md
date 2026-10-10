# Valkey Host packaging comparison — 2026-10-10

Issue #95. Qualifies Valkey `9.1.2` and `9.1.1` for Host-local packaging on the
disposable `provision-acceptance` VM (via `base.local`), restored from its
clean snapshot before and after each run. Selected path: digest-pinned image
under rootless Podman and a Quadlet user unit (ADR 0076).

## Qualified

- Exact digest-pinned image and configuration for each version.
- Rootless Environment-account ownership and Quadlet user unit.
- Loopback-only listener on `127.0.0.1:26379`.
- Real authenticated `valkey-cli` connectivity under an ACL with the default user disabled.
- AOF `appendonly yes`, `appendfsync everysec`, and `aof_enabled:1`.
- Owned generation data path and non-secret credential reference.
- Keys written before a full Host reboot survive it, and writes work after.
- Clean disposable-VM restoration after evidence capture.

## Not qualified

- Store Transition, replication, write fencing, or any rollback guarantee.
- Backup, restore, host-loss recovery, crash durability beyond a clean reboot.
- Derived or ephemeral Store Data Roles, multi-node Valkey, ElastiCache, external Valkey, Redis.
- Valkey versions other than `9.1.1` and `9.1.2`; other image manifests, Podman or OS versions, or CPU architectures.
