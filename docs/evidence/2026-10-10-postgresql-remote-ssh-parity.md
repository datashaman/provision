# PostgreSQL remote SSH parity — 2026-10-10

Issue #76. Repeats the managed PostgreSQL Database tracer from the controller over
strict-host-key SSH and compares it with the direct-local matrix from #75, each run
from the clean `provision-acceptance` snapshot, in `before`, `after`, and
`undecidable` fault modes. Machine-readable record:
[parity record](2026-10-10-postgresql-remote-ssh-parity.json).

## What ran

`scripts/run-remote-ssh-postgresql-acceptance.sh` runs the direct-local matrix, then
`scripts/test-remote-ssh-postgresql-host.sh` per mode (reset, bootstrap, run), then
compares `support-observation.json` of each pair. Host-side setup (artifact install,
HTTP workload reset, unsafe candidate record, undecidable evidence swap) goes through the
same strict SSH identity; every Provision operation goes through the restricted
executor over SSH. The comparison covers host platform, executor digest, PostgreSQL
and Podman versions, and `scripts/postgresql-matrix-accounting.py` output: logical
Database, Store Generation identities, workload records, backup and restore-verification
evidence, retention, operation outcomes, and the owned-resource inventory (Plan-derived
ids normalized). All three modes matched.

Controller: Darwin 24.6.0 `arm64`. Host Target: Ubuntu 26.04 `x86_64`, systemd
`259 (259.5-0ubuntu3.4)`, Podman `5.7.0+ds2-3build1`, PostgreSQL `17.6` → `17.7`
(`sha256:030da09481c3876b71a7e49738a932e1c18c398201a1e4ccfdbff1e5a541215b`). The VM was
restored to its clean snapshot after the definitive run.

## Failure boundaries and recovery

- **Before dispatch:** the controller dies before the executor runs. The journal holds an
  intent with no outcome; resume observes the remote Host and runs the operation once.
- **After remote completion:** the executor finished but the response was lost. Resume
  observes the completed remote effect and records it without replaying
  (`*-unexpected-replay` marker never created).
- **Undecidable:** authority-switch evidence unreadable after the switch. Resume records
  `uncertain` with a recovery action and does not replay the switch.
- A stale executor lineage is rejected by the fencing token.

Cutover is lossless after the bounded write fence, but rollback stays classified
`forward-only`: the retained previous generation is evidence, not a promise of automatic
rollback.

## Executor fixes found by this run

- Database observation shared one 10s budget across ~8 rootless podman calls; the last
  SQL identity query timed out and a healthy active generation was reported
  `unobservable`. Budget is now 60s.
- A candidate can report healthy while the image's init server restarts; version and
  identity `psql` checks now retry for up to 30s.
- Runs were also disturbed by the controller laptop sleeping; run under `caffeinate`.

## Not qualified

RDS, Valkey, object stores, multi-node PostgreSQL availability, production disaster
recovery, external PostgreSQL adoption, automatic cleanup, other controllers, hosts,
SSH, Podman, systemd, or PostgreSQL versions.
