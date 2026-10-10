# PostgreSQL candidate-failure protection and forward-only rollback — 2026-10-10

Proves two boundaries from the #67 spec that the #75/#76 matrices did not exercise live. Run
direct-local on the disposable `provision-acceptance` VM (transport does not change either
property), each scenario from the clean snapshot, via
`scripts/run-direct-local-postgresql-boundaries.sh`. The VM was restored clean afterward.
Same Host, image and versions as the [SSH parity evidence](2026-10-10-postgresql-remote-ssh-parity.md)
(PostgreSQL `17.6` → `17.7`).

## Failed candidate leaves the active generation untouched

1. Provision `17.6` through the CLI; write marker row `pre-transition` into the active Database.
2. Approve the `17.7` transition; execute `prepareDatabaseCandidate` (`op-01`, succeeded).
3. Stop the candidate service out of band, then execute `synchronizeDatabaseCandidate` (`op-02`).
   It fails: "copy PostgreSQL logical snapshot into candidate generation"; the journal records
   outcome `failed`.
4. Observed afterwards: the active generation is still `postgresql-17-6-b86568d3e0fe`, healthy and
   connected; the HTTP workload still serves its record; `pre-transition` is still present; no
   authority-switch evidence exists.
5. Executing `switchDatabaseAuthority` (`op-06`) directly is refused ("operation dependency
   `op-05` has no recorded outcome"); the active generation and the absence of switch evidence
   are re-checked after the attempt.

The refusal in step 5 is Provision's operation-dependency enforcement, not a candidate-specific
check; the proof is that no path from a failed candidate reaches an authority switch.

## Rollback after candidate writes is forward-only

1. Same setup, then run the full transition (`op-01`…`op-14`): backup (`op-04`) and isolated
   restore verification (`op-05`) precede the switch.
2. The Plan states `storeRollbackGuarantee: forward-only`,
   `forwardCutoverGuarantee: lossless-after-bounded-write-fence` and the capability
   `rollback-classification-exposed-separately-from-retention`; the previous generation is
   reported as retained.
3. After cutover, `pre-transition` is present in the new `17.7` active generation (forward
   cutover lost nothing). Insert marker `post-cutover` there.
4. The pre-cutover backup (restored with `pg_restore` from the retained dump) contains
   `pre-transition` and not `post-cutover`; the retained generation was fenced before the switch
   and is not the active data path.

So an acknowledged write accepted after cutover exists only in the new generation. Returning to
retained or backup material would lose it, which is why retention is not advertised as zero-loss
rollback. Declaring a stronger guarantee is rejected by configuration validation (unit test
`zero loss rollback overclaim` in `internal/config`).

## Not shown

Failure injected at other points of a transition (the SSH/direct-local matrices cover transport
loss at each operation); candidate failure after authority switch; a supported reverse
synchronization path (none exists).
