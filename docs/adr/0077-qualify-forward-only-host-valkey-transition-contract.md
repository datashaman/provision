# Qualify forward-only Host Valkey transition contract

**Status: Accepted — 2026-10-11.**

## Context

The managed Host Valkey packaging (ADR 0076) and first generation (#97)
established one authoritative Key-Value Store generation. Issue #99 adds the
first Store Transition: replacing that generation with a same-major,
registered, qualified candidate, mirroring the PostgreSQL tracer (ADR 0074)
for the authoritative Store Data Role only (ADR 0068).

## Decision

A Plan whose declared Valkey `version` selects a different registered image
than the observed active generation is a Store Transition with six typed
operations, each depending on its predecessor and each carrying phase-specific
signed evidence:

1. `prepareKeyValueCandidate`: a separate candidate service, container, Quadlet
   and loopback listener (`127.0.0.1:26380`) on the target generation's data
   path. The candidate identity is derived from the stable one and the executor
   rejects any caller-selected value.
2. `synchronizeKeyValueCandidate`: the candidate replicates from the active
   generation with `REPLICAOF`. It authenticates with the same credential,
   passed to the candidate's own entrypoint, so the executor never handles the
   password for synchronization.
3. `fenceKeyValueWrites`: `ACL SETUSER <user> -@write` on the active generation,
   then `CLIENT KILL USER <user> TYPE normal` and `TYPE pubsub`, bounded at
   `30s`. A probe write must be rejected with `NOPERM` and must not exist
   afterwards. Replication offset equality is then required: the replication
   link is up and the candidate offset equals the active `master_repl_offset`.
   Both offsets are carried in the signed result.
4. `switchKeyValueAuthority`: nothing mutates until the Plan's own fence record,
   a still-rejected probe write and offset equality are re-proven. Then the
   previous service stops, the candidate is promoted with `REPLICAOF NO ONE`,
   the stable Quadlet is rewritten for the new generation, and the stable
   service restarts on the same loopback listener (`127.0.0.1:26379`), so
   logical bindings and the credential reference are unchanged. The candidate
   Quadlet is removed so the candidate unit cannot restart on the now
   authoritative data directory. A failure after the first mutation is
   `uncertain` and pauses with evidence.
5. `verifyKeyValueActive`: exact observation of the new generation as master,
   with the deterministic workload keys read back from it.
6. `retainKeyValuePrevious`: a retention record with the previous generation's
   exact identity, the Transition Cleanup Policy and the rollback window. It
   states `rollbackClaim: none-retention-is-not-a-rollback`.

Both generations must run AOF `appendonly yes` and `appendfsync everysec`; a
generation that does not is rejected at planning and by the executor.

Forward cutover is classified `lossless-after-bounded-write-fence`. Rollback
after candidate writes is `forward-only`: there is no reverse replication, and
the retained previous generation is not a rollback claim.

## Rejected alternative

`CLIENT PAUSE ... WRITE` is rejected as the fence. It blocks writers instead
of rejecting them and expires on its own, so a blocked write can resume and be
acknowledged on the old generation after authority switches. ACL revocation
fails writes immediately and does not expire.

## Consequences

- The lossless claim is about the forward cutover only. AOF `everysec` can lose
  about one second of writes on a Host crash, and nothing here qualifies crash
  durability, host-loss recovery, backup or restore.
- The application ACL user is the credential's single user. Revoking `-@write`
  leaves the administrative commands the executor needs (`ACL`, `CLIENT`,
  `INFO`, `REPLICAOF`). A separate admin user remains deferred.
- A restart of the active service during the fence window reloads the ACL file
  and re-opens writes. The switch re-proves the fence immediately before it
  mutates, but a restart between that check and stopping the previous service is
  not excluded.
- Deterministic keys are namespaced by revision. A transition must use the
  revision that prepared them, otherwise `prepareKeyValueCandidate` fails
  closed before any mutation.
- The candidate reaches the active container by its address on the shared
  rootless Podman network, because host loopback is not reachable from a
  container.
- A fence failure after the ACL revocation is `uncertain` and leaves the
  application locked out of writes until an operator resumes or recovers; there
  is no automatic un-fence.
- A recorded fence is reused only while a probe write is still rejected. A
  switch that completed every mutation but crashed before writing its evidence
  marker resumes by recording the marker from the Plan's fence record.
- The candidate's replication password is passed as server options, so it is
  visible in the candidate process arguments to the Environment account and
  root; a `masterauth` file is a later hardening.
- Replacing the active generation under an HTTP-bound workload is not
  qualified; transition the Key-Value Store on its own.
- Excluded: derived and ephemeral roles, multi-node Valkey, ElastiCache,
  external Valkey, cross-major upgrade, Redis, and any zero-loss or
  bounded-loss rollback.
- Live disposable-Host proof (interruption modes, SSH parity, candidate failure)
  is the work of #100–#102. Until then SUPPORT_MATRIX qualifies the contract
  shape only.
