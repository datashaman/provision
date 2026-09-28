# Prove Host PostgreSQL backup and isolated restore verification

**Status: Accepted — 2026-09-28.**

## Context

The first managed Host PostgreSQL tracer can prepare a Store Generation and
can execute a forward-only Store Transition, but neither path proved backup
evidence or restore verification. A backup claim is not enough for
authoritative data: Provision needs evidence outside the active physical
generation and restore verification in an isolated candidate before claiming a
recoverable Database path.

## Decision

Qualify a narrow Host PostgreSQL backup and restore-verification contract:

- authoritative managed PostgreSQL configuration declares backup frequency,
  retention, destination, recovery-point objective, recovery-time objective,
  restore-verification mode, and host-loss expectation;
- host-local backup execution supports `file://` destinations only when
  host-loss recovery is `not-declared`;
- declaring `hostLoss: off-host-backup-required` with a same-host `file://`
  destination fails validation;
- `ssh://` destinations are recognized as off-host contract shape, but the
  Host renderer does not execute that transfer yet;
- initial and already-authoritative Database Plans run `backupDatabase` and
  `verifyDatabaseRestore` against the active generation;
- Store Transition Plans run `backupDatabase` and `verifyDatabaseRestore`
  after the bounded write fence and before `switchDatabaseAuthority`, so the
  restore proof exists before any authority cutover;
- backup evidence records non-secret destination identity, destination class,
  source generation, achieved recovery point, format, and proof that the
  backup path is outside the active Store Generation; and
- restore verification materializes the backup into a separate restore
  candidate identity and verifies it without changing Database authority.

Destructive in-place restore remains unavailable.

## Consequences

- A same-host backup is useful evidence for backup mechanics, accidental
  generation loss, and restore verification, but it is not host-loss recovery.
- Off-host host-loss recovery is now a validation boundary rather than a vague
  implication.
- Plans for otherwise unchanged Databases may still contain backup and restore
  verification operations. That is intentional maintenance work, not an
  application rollout.
- Later work must add executable `ssh://` transfer evidence or provider-native
  backup support before claiming Host-loss recovery.
