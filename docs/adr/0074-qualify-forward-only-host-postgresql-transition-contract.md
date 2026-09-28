# Qualify forward-only Host PostgreSQL transition contract

**Status: Accepted — 2026-09-28.**

## Context

The managed Host PostgreSQL packaging proof established one exact
PostgreSQL 17.6 generation, but it deliberately did not qualify a Store
Transition. Issue #73 needs the first executable shape for replacing an active
Database Store Generation without weakening Provision's stateful blue-green
language.

## Decision

Qualify the first Host PostgreSQL Store Transition contract narrowly:

- active and candidate generations must both be the exact qualified PostgreSQL
  17.6 image and manifest;
- the candidate uses a separate candidate service, container, and loopback
  listener before authority switch;
- the Plan exposes candidate preparation, candidate synchronization, bounded
  write fence, authority switch, active verification, and previous-generation
  retention as separate typed operations;
- the bounded write fence is the specific
  `active-database-read-only-with-session-termination` contract with a `30s`
  maximum statement bound before the final logical snapshot;
- DB-bound workloads that declare deterministic record IDs must prove those
  records directly from PostgreSQL during active Database verification;
- forward cutover is classified as
  `lossless-after-bounded-write-fence`;
- rollback after candidate writes is classified as `forward-only`; and
- incompatible active generations, unsafe candidates, or unknown compatibility
  evidence still fail closed before authority changes.

This qualifies a transition contract in the Plan, restricted executor, and
local result verifier. It does not yet qualify PostgreSQL major-version
upgrade, logical-replication DDL or sequence restrictions, reverse
synchronization, automatic zero-loss rollback, backup/restore, host-loss
recovery, RDS, or production durability.

## Consequences

- Host inspection may advertise
  `offline-logical-snapshot-with-bounded-write-fence` only for the exact
  supported PostgreSQL packaging.
- Generic healthy PostgreSQL observations are insufficient for transition
  success; every transition result must carry the phase-specific evidence the
  Plan requested.
- The support matrix must distinguish this contract-level transition support
  from the earlier live packaging proof.
- Later live acceptance should add fault-injection evidence for interruption,
  stale fencing, deterministic-record preservation, and ambiguous data-bearing
  recovery before expanding the production support claim.
