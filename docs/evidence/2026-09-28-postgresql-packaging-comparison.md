# PostgreSQL Host packaging comparison — 2026-09-28

**Decision state: approved and live-qualified for packaging only. No production
Database transition, backup, restore, or host-loss recovery guarantee is implied
by this evidence.**

Issue #68 selected and proved the first managed Host-local PostgreSQL packaging
for the Database tracer. The accepted packaging is a digest-pinned official
PostgreSQL OCI image run by rootless Podman/Quadlet under one Environment
account, with one owned data path per physical generation and credentials
delivered through systemd's encrypted credential mechanism.

## Selected identity

| Field | Value |
| --- | --- |
| PostgreSQL version | `17.6` |
| OCI image | `docker.io/library/postgres@sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409` |
| Image index | `sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929` |
| Linux `amd64` manifest | `sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409` |
| Environment account | `provision-lab` |
| Unit | `provision-lab-postgresql.service` |
| Container | `provision-lab-postgresql` |
| Generation | `postgresql-17-6-b86568d3e0fe` |
| Data path | `/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-17-6-b86568d3e0fe/data` |
| Listener | `127.0.0.1:25432` |

The approval record is
[`2026-09-28-postgresql-packaging-approval.json`](2026-09-28-postgresql-packaging-approval.json).
Its SHA-256 digest during the live proof was
`4addc32b3819a23d4fc16a6ce13128b1715baee047eca22d1092fb0168e797fe`.

## Why not a Host-wide package?

Ubuntu 26.04 offered PostgreSQL packages, including native PostgreSQL 18
candidates, but selecting a Host-wide package would make Provision mutate a
shared system service and package transaction in place. That is the wrong first
managed-store shape for Provision: later Database Plans need exact physical
generations, independent data paths, explicit candidate preparation, and
forward-only store transition semantics.

The selected OCI/Quadlet packaging keeps the managed PostgreSQL service scoped
to one Environment account and one physical generation. Host-installed
PostgreSQL remains a possible external component, but it is not the curated
managed Host-local implementation.

## Live proof

The disposable `provision-acceptance` VM was restored from the clean Incus
snapshot before the proof. The controller then installed only the approved
Podman package set, created the `provision-lab` Environment account, wrote the
Quadlet unit, delivered the PostgreSQL URL as an encrypted user-scoped systemd
credential, started PostgreSQL, inserted a durable probe row, rebooted the Host,
verified the same data after reboot, copied evidence, and restored the VM back
to the clean snapshot.

Evidence captured by
`/tmp/provision-issue68-evidence-20260928T022155Z`:

| Evidence | Result |
| --- | --- |
| Controller result | `PostgreSQL acceptance controller passed` |
| Qualification result | `passed` |
| Qualification digest | `sha256:892fb587ac7323ba4f04d38b1fc165f9e2304219f3de14e7e365cb60b4960eff` |
| Pre-prepare clean observation | `provision-lab` absent, Podman absent, issue evidence absent |
| Post-restore clean observation | `provision-lab` absent, Podman absent, issue evidence absent |
| PostgreSQL version after reboot | `17.6 (Debian 17.6-2.pgdg13+1)` |
| SQL connectivity | `provision_issue68|provision_issue68` |
| Durable data after reboot | `after-reboot`, `before-reboot` |
| Listener | `127.0.0.1:25432` |

The proof harness digest was
`sha256:b1fd8d285ba3ab14a6b906eecc0f6f436f4ed97b46a0fb472d056bf402d051f3`.
The controller harness digest was
`sha256:e578c193fed177289933f7da803df8db046004c03d8e435ef9fe5dfae278ee70`.

## Credential boundary

The proof qualifies non-secret durable references plus systemd encrypted
credential delivery into the PostgreSQL unit. It does not claim that the VM's
underlying block device or hypervisor backing store is encrypted. A production
Host requiring encrypted media must advertise and prove that separately.

## Qualified

- Exact product and image identity for PostgreSQL `17.6`.
- Rootless Environment-account ownership.
- Quadlet user unit generation.
- Loopback-only PostgreSQL exposure.
- SQL connectivity using the planned database and user.
- Owned generation data path.
- Non-secret credential reference in durable records.
- Automatic restart and durable data survival across one full Host reboot.
- Clean disposable-VM restoration after evidence capture.

## Not qualified

- Production Database operations.
- Backup or restore.
- Host-loss recovery.
- Multi-node availability.
- Side-by-side store transition, replication, or write fencing.
- Automatic rollback after candidate writes.
- RDS or external PostgreSQL.
- PostgreSQL versions other than `17.6`.
- Other image manifests, Podman versions, OS versions, or CPU architectures.
