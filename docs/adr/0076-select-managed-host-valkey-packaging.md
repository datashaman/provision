# Select managed Host Valkey packaging

**Status: Accepted — 2026-10-10.**

## Context

The Valkey Key-Value Store tracer (#93) needs one managed host-local Store
Generation before any Store Transition is planned. As with PostgreSQL
(ADR 0073), packaging fixes the immutable product identity, rootless service
identity, owned generation data path, credential delivery, and persistence
mode that later Plans bind.

## Decision

Package the curated managed Host Valkey implementation as a digest-pinned OCI
image run by rootless Podman and a Quadlet user unit under the dedicated
Environment account, with systemd as the lifecycle manager. Tag-only image
references are rejected. Two same-major images from `docker.io/valkey/valkey`
are qualified so a later candidate generation can differ from the active one:

| Version | Image index | Linux `amd64` manifest | Generation |
| --- | --- | --- | --- |
| `9.1.2` (default) | `sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd` | `sha256:ac9c858b86dc81f3482d53a9d854547379a5f41e012500f839ab257c399063e0` | `valkey-9-1-2-ac9c858b86dc` |
| `9.1.1` | `sha256:64e361b630ecf18dff7ca4df6a88e6eafc193687eb48cff2c7e0a293ab67d29a` | `sha256:cc03bc07761acc552c960bfbcc66196dcf02e949dadb92d941d970ed6c782fea` | `valkey-9-1-1-cc03bc07761a` |

Shared identity: service `provision-lab-valkey.service`, container
`provision-lab-valkey`, Environment account `provision-lab`, loopback listener
`127.0.0.1:26379`, and data path
`/var/lib/provision/environments/lab/services/valkey/generations/<generation>/data`.

Every managed generation runs with AOF persistence, `appendonly yes` and
`appendfsync everysec`; the proof fails closed if either differs. The
credential is a systemd encrypted, user-scoped credential referenced by
non-secret reference. The container starts an ACL file with the default user
disabled and a single admin user whose password hash is derived at start. The
split between an application user and an admin user, needed for the Store
Transition write fence, is deferred to the transition slice.

## Consequences

- This packaging proof advertises no executable Store Transition, lossless
  cutover, or Store Rollback Guarantee.
- AOF `everysec` can lose about one second of writes on a Host crash. The
  proof qualifies persistence across a clean reboot only; it makes no
  crash-durability or host-loss claim.
- Only the `authoritative` Store Data Role is in scope for the tracer; derived
  and ephemeral managed Valkey remain unqualified.
- The registry in `internal/implementation/hostkeyvalue` carries each image's
  qualification digest, and a unit test checks it against the committed
  evidence.

## Rejected alternative

Ubuntu's native `valkey-server` package is not selected: it is host-wide
package state, couples all Environments on the Host to one package
transaction, and has no Environment-scoped immutable runtime identity or
generation-specific data directory.

## Acceptance evidence

Approval records:
[`9.1.2`](../evidence/2026-10-10-valkey-9-1-2-packaging-approval.json),
[`9.1.1`](../evidence/2026-10-10-valkey-9-1-1-packaging-approval.json).
Qualifications:
[`9.1.2`](../evidence/2026-10-10-valkey-9-1-2-packaging-qualification.json),
[`9.1.1`](../evidence/2026-10-10-valkey-9-1-1-packaging-qualification.json).
The proof harness is
[`prove-valkey-quadlet-host.sh`](../../scripts/prove-valkey-quadlet-host.sh),
driven by
[`run-valkey-quadlet-acceptance.sh`](../../scripts/run-valkey-quadlet-acceptance.sh).
Both proofs passed on 2026-10-10 on the disposable Ubuntu 26.04 VM restored
from its clean snapshot, which restored clean afterward. The support note is
[`2026-10-10-valkey-packaging-comparison.md`](../evidence/2026-10-10-valkey-packaging-comparison.md).
