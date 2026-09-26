# Rejected Worker candidate evidence — 2026-09-26

Issue #41 was exercised against the existing issue #40 deployment on the
disposable `provision-acceptance` VM with the checked-in
`scripts/test-rejected-worker-candidate-host.sh` harness. The seven-stage run
ended with `rejected Worker candidate acceptance passed` and retained its
structured controller evidence under `/tmp/provision-issue41-evidence`.

## Tested combination

| Property | Observed value |
| --- | --- |
| Controller | macOS `15.7.7`, Darwin `24.6.0`, `arm64` |
| Provision source and binary | engine through commit `4405242`; macOS `arm64`, `sha256:49560554d28a6c9639ca2361cc1c733d9a00757e7d363293673c3c4bbea04982` |
| Acceptance harness | commit `059071c` |
| Restricted executor | Linux `x86_64`, `sha256:a0f63e29bf5f8aa231f20fea7359b5f544093457bec68f8374bca60682010a1a` |
| Host Target | Ubuntu Server `26.04`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Management transport | strict SSH host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Authorization | authority key ID `sha256:5ded445e242a8c5aca90da032b444d10ec94935b07a29f81613ddc3ba0a2be95` |
| Queue runtime | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` |
| Active Worker | `provision-example-async` v0.1.1, `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40` |
| Candidate Worker | `provision-example-async` v0.2.0, `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` |
| Plan | `sha256:a46b2c024771b6e5640f1493d1ee5b19772aedab90e36a16f4c555169bb11783`; five signed candidate-only operations |

## Proven rejection path

The Worker-only Plan retained the exact logical and physical Queue Generation,
staged the v0.2.0 Worker Artifact, and installed it under the separate immutable
unit `provision-lab-consumer-14caeac0dbda.service`. The candidate started with
its root-owned admission gate closed. Restricted inspection required that gate
file and the Worker's runtime state to agree, then observed the candidate
process alive and connected to RabbitMQ while its in-flight count remained
zero; the v0.1.1 active Worker stayed alive with its gate open.

While both generations were running, scheduled Task Invocations
`inv-e89272bec56ea062e16c3071` and `inv-e3d40cf56b50bd363ec091d0`
published messages `msg-ab1c162c547a799da2574d219a8aaf30d419f84b9c952501a0e9b63d46578d4b`
and `msg-11e388fdbe76a30b0a9fd45439a24ae9d7f4913cadd23dd967ec87eaaeea9547`.
Each complete Worker history contains only the v0.1.1 active Worker's
`received`, `processed`, `acknowledgement_decided`, and `acknowledged` events.
The gated candidate neither claimed nor settled either message.

The harness then stopped only the candidate unit before executing
`verifyWorkerCandidate`. Verification failed with structured liveness, Queue
connectivity, Revision identity, and intake-state checks plus safe systemd
diagnostics. The approved candidate-only Plan contained no intake-fence, drain,
activation, active-verification, or retention mutation; attempts to name those
operations were rejected as absent from the exact approved Plan. The final
observation retained the failed candidate separately with its gate closed,
`candidate-unverified` health, a reason, and an operator recovery action.

The active Worker identity and open gate, retained-Worker state, Queue ID and
Generation, Task Generation, and Schedule binding all remained unchanged. The
attempt neither removed nor mutated an active or retained Worker Generation.

## Diagnostic and credential boundary

The failed operation reported its exact Plan operation and kind, the failed
verification checks, the candidate unit, safe systemd state, and the recovery
instruction. The Configuration, Plan, approval, journal, operation results,
status, and retained harness output did not contain the resolved RabbitMQ
password.

## Evidence identities

| File | SHA-256 |
| --- | --- |
| `plan.json` | `884d7c6d8b2863baf06280268ddc19cc803c075aa758961d97c865faa449d7f7` |
| `approval.json` | `6bb0aa6875857649f6f9ddd0b1cf41b85e4d7732ed9a6c961246ab676995da1c` |
| `op-07-failed.json` | `2c36597ae5fb091473055db5ea4afdbf61e1feac9bb6e975f667a4c8065c1888` |
| `deployment-status.json` | `1962df1fca5b77fd0f573f359bf6506fc214c3b5358b3e469455cf0ce32c8809` |
| `bootstrap-after.json` | `f824891827354cbdff374b0042b32c0ae6364ecec5673279670fe5a9e21c4312` |

This evidence qualifies rejection before Worker intake handoff on the exact
single-Host combination above. It does not qualify a successful Worker handoff,
draining an old Worker, retained-generation rollback, crash-boundary recovery,
Queue replacement, multi-host availability, EC2, ECS, Lambda, or SQS.
