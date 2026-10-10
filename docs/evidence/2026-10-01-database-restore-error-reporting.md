# Database restore-verification failure reporting

Issue: [#88](https://github.com/datashaman/provision/issues/88).

## Decision

A failed or uncertain `verifyDatabaseRestore` result may arrive before the
isolated candidate exists or is observable. Its Database observation can be
empty or describe the source generation. The controller must validate the
result's authorization and structured failure diagnostics without requiring
successful candidate identity evidence. It journals the original outcome and
observation, including the Host's reason, failure category, and recovery action,
and includes the reason in the returned error.

Successful restore verification still requires the exact planned isolated
candidate, verified restore evidence, and applicable deterministic workload
records. No authority cutover or automatic replay is introduced.

## SQL-check timeout and retry decision

Keep the existing 10-second Host PostgreSQL inspection bounds unchanged for
this fix; do not add automatic retries or a configuration option. The current
failure report does not establish whether elapsed inspection time, container
startup, restore execution, or another precondition caused a failure. Preserving
the Host's diagnostics is needed before changing those bounds. The current
bounds cover multiple inspection commands, including SQL checks, rather than
only a single SQL query.

If captured failures demonstrate a need, handle configurable inspection budgets
or bounded, read-only inspection retries as a separate change with an explicit
Plan/operation deadline contract and slow-Host tests. Do not retry data-bearing
restore mutations blindly: uncertain execution must continue to observe before
retrying, per ADR 0047.

## Regression coverage

- Failed and uncertain restore results with empty, source, or candidate Database
  observations retain their diagnostic contract.
- Missing structured diagnostics and mismatched result authorization are rejected.
- Successful results with empty or source identities are still rejected.
- Engine execution with the production Host verifier preserves an empty-Database
  failure's reason in both the returned error and the SQLite operation journal,
  preserving failed versus uncertain outcomes.
