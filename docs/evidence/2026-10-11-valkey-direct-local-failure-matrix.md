# Valkey direct-local Store Transition failure matrix — 2026-10-11

Live proof for #100, run direct-local on the disposable `provision-acceptance` VM (Ubuntu 26.04,
Linux 7.0.0-34, Podman 5.7.0, rootless Quadlet) via `scripts/run-direct-local-valkey-transition-matrix.sh`,
from the clean committed source `bcd15da` (the evidence records exactly that version, no `-dirty-`
suffix). Each fault mode starts from the clean snapshot. State is CLI-provisioned only: Valkey `9.1.1`
is prepared by `prepareKeyValue`, the Environment is then retargeted at `9.1.2`, and the six-operation
Store Transition ([ADR 0077](../adr/0077-qualify-forward-only-host-valkey-transition-contract.md)) runs
with every operation interrupted. Structured evidence:
[JSON](2026-10-11-valkey-direct-local-failure-matrix.json); the `faultRecord` and `uncertainOutcomes`
fields hold the per-operation proof below.

| Mode | Interruption | Result |
| --- | --- | --- |
| `before` | CLI killed before the restricted executor runs, around each of the six operations | Each operation pauses with attempt and fencing evidence; `resume` observes the Host first, executes once and succeeds. |
| `after` | CLI killed after the executor completed, around each of the six operations | Each operation pauses; `resume` observes the completed mutation and succeeds without invoking the executor again (`mutationReplayedOnResume: false` for every operation). |
| `undecidable` | Only `switchKeyValueAuthority` (`op-04`) gets the undecidable injection; the other five run as `after` | The authority-switch evidence is made unreadable. Resume exits non-zero and records outcome `uncertain` (`ambiguous-data-bearing-state`) with a reason and a `recoveryAction`, and does not replay the switch: the authoritative container's `StartedAt` is unchanged. After the evidence is restored, explicit recovery completes the switch. |

**Why `undecidable` covers one operation.** It targets the only transition step whose post-condition
can be ambiguous: a switch that mutated the Host but could not prove or record that it did. The other
five either leave no durable marker to make unreadable, or re-observe and converge when their marker is
unreadable, so they cannot produce an undecidable observation. This mirrors the PostgreSQL matrix, which
also injects it only at its authority switch. The `faultRecord` in the JSON states the injection applied
to each operation.

Observed in every mode: ACL write fence (`acl-write-revocation-with-client-kill`) with equal active
and candidate replication offsets and the link up; authority switch to the new generation on the
unchanged `127.0.0.1:26379` listener; both deterministic workload keys read back from it; retention
of the `9.1.1` generation as `forward-only` / `retain-previous-generation`; and the resolved credential
absent from all evidence.

## Comparable accounting

The normalized accounting (logical store `provision-lab-sessions`, initial/active/retained Store
Generation identities, workload keys before and after, fence, switch and retention results,
per-operation outcomes, and the owned-resource inventory) is identical across the three modes; the run
asserts this. Two fields are excluded because they differ by design or time:

- `faultRecord` / `uncertainOutcomes` differ because each mode injects different faults.
- `replicationAtFence` offsets were 100, 142 and 114 bytes for `before`, `after` and `undecidable`.
  The gaps are multiples of 14 bytes, the length of a replication `PING`, which is consistent with
  periodic replica pings advancing with each run's wall time. That explanation is not independently
  verified. Offset equality between active and candidate held in every run.

## VM restore

The runner restores the clean snapshot before every mode and after the last one, then checks that
`/var/lib/provision/environments/lab` is absent; the result is recorded as `vmRestore` in the JSON.

## Defect found by this run

#99 resolved the active generation's container IP for `REPLICAOF`. Under rootless Podman with pasta
networking containers have no inspectable address and cannot reach host loopback, so
`synchronizeKeyValueCandidate` failed live. The candidate now shares the host network namespace, binds
only `127.0.0.1:26380`, and replicates from the active `127.0.0.1:26379`. That fix is its own change
(ADR 0077 edited in place) and this matrix depends on it.
