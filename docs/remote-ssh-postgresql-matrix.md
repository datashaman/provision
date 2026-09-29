# Remote SSH PostgreSQL matrix

The managed PostgreSQL tracer runs the same lifecycle from a separate controller as it does directly on the Host Target. Provision, the authority private key, approvals, the SQLite State Backend, and the resolved Database credential stay on the controller; the disposable Linux Host Target receives only strict-SSH observation requests and signed typed operations for its restricted executor. Support is qualified only for the exact combination in the [support matrix](SUPPORT_MATRIX.md#managed-postgresql-ssh-parity).

## User workflow

1. Declare the Environment with a `kind: host` target that has `address` and `user`, and a `postgresql-quadlet` implementation with `lifecycle: managed`, a `credential` reference, and optionally `version: "17.7"`.
2. Pin the Host's ED25519 key in the controller's OpenSSH `known_hosts` after verifying it out of band. Provision uses batch mode with strict host-key checking and never falls back to a password.
3. Bootstrap the Host once (`scripts/bootstrap-host.sh`) with the authority public key.
4. `provision plan preview`, then `provision plan approve`. The Plan is bound to the observed Host state.
5. `provision deployment execute --operation op-NN` for each operation, or `provision deployment resume` after an interruption. Each execution passes the credential with `--secret-file secret://…=FILE`; the value is never journaled.

Changing the Database `version` from `17.6` to `17.7` produces a Store Transition Plan: candidate generation creation, synchronization, an explicit bounded write fence, backup, isolated restore verification, authority switch, post-switch verification, and retention marking of the previous generation.

## Failure boundaries and recovery

The harness runs the transition under three transport-loss modes selected with `--fault-mode`:

| Mode | Where the controller or transport is lost | Required result |
| --- | --- | --- |
| `before` | The SSH client is killed before the executor request reaches the Host, for every transition operation | The journal ends at an intent with attempt and fencing evidence and no outcome. After the lease expires, resume observes the Host, finds nothing done, and executes the operation once. |
| `after` | The Host completed the operation and the controller is killed before it reads the response | The journal keeps the intent. Resume observes the remote Host, proves completion from Host evidence, and records the outcome without dispatching the mutation again (no replay marker appears). |
| `undecidable` | As `after`, and the authority-switch evidence on the Host is then made unreadable | Resume cannot tell whether the switch landed, does not replay it, and records the outcome `uncertain` with a reason and recovery action. Once the evidence is readable again, resume completes. |

`before` and `after` interrupt each of the eight transition operations: Database generation creation, synchronization, the write fence, backup, restore verification, the authority switch, post-switch verification, and retention marking. `undecidable` runs as `after` and additionally makes the authority-switch evidence unreadable for that operation only. Recovery always observes the remote Host before acting, and a stale-lineage executor request is rejected by the Host's fence.

## Forward cutover and rollback

A successful transition is a **lossless forward cutover**: writes are fenced for at most `30s`, the candidate is synchronized to the fence point, and authority moves with no acknowledged write lost. This is not a rollback promise. Store rollback is classified `forward-only`: after the switch the previous generation is retained as evidence and for operator-directed restore, but Provision does not switch authority back automatically, because writes accepted by the new generation would be lost. A backup and isolated restore verification precede the switch so that a recovery source exists.

## Run it

```sh
./scripts/run-remote-ssh-postgresql-acceptance.sh
```

The wrapper runs local checks, then the direct-local matrix, then the SSH matrix for each mode. Each run starts by restoring the acceptance VM's clean snapshot and bootstrapping it, so both transports see an identical clean Host. It writes `parity-summary.json`, which fails unless the logical Database, Store Generation identities, workload records, backup evidence, restore verification, retained material, and owned-resource inventory are identical, then restores the clean snapshot. Individual runs use `scripts/test-remote-ssh-postgresql-host.sh` against an already bootstrapped disposable Host.

The recorded results are in the [2026-09-29 SSH parity evidence](evidence/2026-09-29-remote-ssh-postgresql-parity.md).
