# Remote SSH PostgreSQL parity — 2026-09-29

Issue #76. Proves that the managed PostgreSQL tracer behaves the same over strict-host-key SSH from a separate controller as it does directly on the Host Target, on identical clean snapshots of the `provision-acceptance` VM. Structured record: [`2026-09-29-remote-ssh-postgresql-parity.json`](2026-09-29-remote-ssh-postgresql-parity.json).

## What ran

`scripts/run-remote-ssh-postgresql-acceptance.sh` (exit 0), which:

1. ran `bash -n`, `shellcheck`, `go test ./...`, and `go vet ./...` on the controller;
2. ran the direct-local matrix (`scripts/run-direct-local-postgresql-acceptance.sh`) in `before`, `after`, and `undecidable` modes, each from a freshly restored and bootstrapped VM;
3. ran `scripts/test-remote-ssh-postgresql-host.sh` in the same three modes from the controller, each from a freshly restored and bootstrapped VM;
4. compared each mode's `accounting.json` and Host identity between the two transports, and failed on any difference;
5. restored the VM to its clean snapshot.

Both matrices provision PostgreSQL `17.6` through the CLI, then run the forward-only Store Transition to `17.7`, interrupting every transition operation (`prepareDatabaseCandidate`, `synchronizeDatabaseCandidate`, `fenceDatabaseWrites`, `backupDatabase`, `verifyDatabaseRestore`, `switchDatabaseAuthority`, `verifyDatabaseActive`, `retainDatabasePrevious`) before dispatch, after Host completion, or with unreadable authority-switch evidence.

## Parity result

For all three modes the direct-local and remote runs have identical: logical Database (`provision-lab-data`), initial and active Store Generation identities (`postgresql-17-6-b86568d3e0fe` → `postgresql-17-7-030da09481c3`), retained previous generation, workload record (`…/record-0001`), backup evidence shape (outside the active generation), isolated restore verification, owned-resource inventories, write fence, transition phases, and journal event sequences including resume provenance.

## Environment

| Item | Value |
| --- | --- |
| Controller | macOS (`Darwin 24.6.0`), `arm64`, `OpenSSH_9.9p2, LibreSSL 3.3.6` |
| Host Target | Ubuntu Server 26.04, `x86_64`, systemd `259 (259.5-0ubuntu3.4)`, Podman `5.7.0+ds2-3build1`, `OpenSSH_10.2p1 Ubuntu-2ubuntu3.6` |
| SSH identity | strict host-key checking, password authentication disabled, pinned ED25519 fingerprint `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Executor | `sha256:085f1824aaab5b1c86eb30fc077d855eb44ed88a0c0d25129092df4278f7bd61` (identical in both transports) |
| PostgreSQL | `17.6` then `17.7`; single node, host failure tolerance 0 |
| Source | The run recorded `sourceVersion` `3875e90…-dirty-a05c569f…`: parent commit `3875e90` plus the uncommitted scripts later committed as `a1eb3b9`. Only documentation changed after the run. The two Provision binaries differ by design (controller `darwin/arm64` for the remote run, `linux/amd64` for the direct-local run on the Host). |

## Limits of this evidence

- The topology (`databaseNodes: 1`, `hostFailureTolerance: 0`) is the declared single-node topology, not something the Host reports.
- "Recovery observes the Host before acting" is shown by the journal shape (interrupted intent, resume provenance, no replay marker in `after`), not by an independent ordering assertion.
- The compared per-run accounting, check logs, and raw transcripts live in the gitignored `work/issue-76-live/evidence`; the committed JSON records the comparison results and identities, not the raw accounting.
- The clean-snapshot restore is performed by the wrapper's final `reset-acceptance-vm.sh` call, which the wrapper must complete for exit status 0; it is not a separate recorded field.

## Defects and flakes found

- Two intermittent failures occurred in earlier SSH-mode attempts on a freshly force-restored VM: a `verifyDatabaseRestore` result rejected with "host Database restore observation does not match the Plan", and a Plan preview rejected with "PostgreSQL database identity cannot be verified". Both passed on isolated reruns. The Host's SQL checks are bounded to 10 seconds, so the runner now waits for a quiet load average after restore before bootstrapping. This is a harness mitigation, not a product fix: a failed restore is reported to the operator as a generic observation mismatch, which hides the underlying cause and is a diagnosability gap worth a follow-up.
- The VM's graceful `incus stop` intermittently timed out; the local reset script now falls back to `--force`.

## Not qualified

RDS, Valkey, object stores, multi-node PostgreSQL availability, production disaster recovery, external PostgreSQL adoption, automatic cleanup, and untested host, runtime, PostgreSQL, or Podman versions.
