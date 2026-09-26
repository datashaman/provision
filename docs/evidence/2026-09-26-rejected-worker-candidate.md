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
| Provision source and binary | engine through commit `cc65846`; macOS `arm64`, `sha256:ff4c5b271a09629a2c476d4b5c790f00c7f30f2d3747670efdd2c1055799669c` |
| Acceptance harness | commit `059071c` |
| Restricted executor | Linux `x86_64`, `sha256:473911a8d8dd485237a3be6ebe3f7996f5629e2f717c0cbccf01e2c56ebeefbd` |
| Host Target | Ubuntu Server `26.04`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Management transport | strict SSH host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Authorization | authority key ID `sha256:5ded445e242a8c5aca90da032b444d10ec94935b07a29f81613ddc3ba0a2be95` |
| Queue runtime | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` |
| Active Worker | `provision-example-async` v0.1.1, `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40` |
| Candidate Worker | `provision-example-async` v0.2.0, `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` |
| Plan | `sha256:7836eb409e010b3ecb9dae7a7b3a0cf4445e26588230b7cae9da354521540b14`; five signed candidate-only operations |

## Proven rejection path

The Worker-only Plan retained the exact logical and physical Queue Generation,
staged the v0.2.0 Worker Artifact, and installed it under the separate immutable
unit `provision-lab-consumer-14caeac0dbda.service`. The candidate started with
its root-owned admission gate closed. Restricted inspection required that gate
file and the Worker's runtime state to agree, then observed the candidate
process alive and connected to RabbitMQ while its in-flight count remained
zero; the v0.1.1 active Worker stayed alive with its gate open.

While both generations were running, scheduled Task Invocation
`inv-e5e61160b25fc802bc4c13af` published message
`msg-d27c188620d047377854c8b194b6b71f9869305908489dd36ebae2ec038c19a4`.
Its complete Worker history contains only the v0.1.1 active Worker's
`acknowledgement_decided` and `acknowledged` events. The gated candidate neither
claimed nor settled it.

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
| `plan.json` | `bab078e1e075868994913ede6f753eab7130be01fd2d90a380f197387f25bfcc` |
| `approval.json` | `c01ba6e75af827069ddb986c282ddf4001e825c351b4ac63c4cd104ec4381e54` |
| `op-07-failed.json` | `3edf9c0fa82acbe51a4b45ea156329d2dfe3b5bc2c3fd1cb0b64453c9e494698` |
| `deployment-status.json` | `05088a19d8cd7d9b03927ce524ee4a6bc06fff9fc4361a4bfb10e4f9981254bb` |
| `bootstrap-after.json` | `49186bfdf4863fee5682893a9f1fa522cf68340431836b8d197e8af5a72900dc` |

This evidence qualifies rejection before Worker intake handoff on the exact
single-Host combination above. It does not qualify a successful Worker handoff,
draining an old Worker, retained-generation rollback, crash-boundary recovery,
Queue replacement, multi-host availability, EC2, ECS, Lambda, or SQS.
