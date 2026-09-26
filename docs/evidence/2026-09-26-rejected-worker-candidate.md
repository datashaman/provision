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
| Provision source and binary | engine through commit `500c187`; macOS `arm64`, `sha256:fa71c9cda478ced13591be20d44f6c8cb5a34332c225fa59bc8894399cb5c71e` |
| Acceptance harness | commit `f94b793` |
| Restricted executor | Linux `x86_64`, `sha256:c1b98d448cfc3878e72c1a3731f62a4efab2f4dbe37b28075da6717fab200965` |
| Host Target | Ubuntu Server `26.04`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Management transport | strict SSH host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Authorization | authority key ID `sha256:5ded445e242a8c5aca90da032b444d10ec94935b07a29f81613ddc3ba0a2be95` |
| Queue runtime | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` |
| Active Worker | `provision-example-async` v0.1.1, `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40` |
| Candidate Worker | `provision-example-async` v0.2.0, `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` |
| Plan | `sha256:c3ed9fb89af53b84ce54bafc0d7566fa600532b8f0908337b9f611b3423c3af7`; ten signed operations |

## Proven rejection path

The Worker-only Plan retained the exact logical and physical Queue Generation,
staged the v0.2.0 Worker Artifact, and installed it under the separate immutable
unit `provision-lab-consumer-14caeac0dbda.service`. The candidate started with
its admission gate closed. Restricted inspection observed the candidate process
alive and connected to RabbitMQ while its in-flight count remained zero; the
v0.1.1 active Worker stayed alive with its gate open.

While both generations were running, scheduled Task Invocation
`inv-d126c812eca04d651ddc6fe4` published message
`msg-f38cf43ab6b42e58fef2c32c25ebd9edfd6c1d210e2b092c0bdbf11012c3a9da`.
Only the v0.1.1 active Worker recorded and broker-acknowledged that message. The
gated candidate did not settle it.

The harness then stopped only the candidate unit before executing
`verifyWorkerCandidate`. Verification failed with structured liveness, Queue
connectivity, Revision identity, and intake-state checks plus safe systemd
diagnostics. Every dependent intake-handoff operation remained ineligible. The
final observation retained the failed candidate separately with its gate closed,
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
| `plan.json` | `731b8672c46506eebb694def41e7b470e7107c675ff07ec15ebe9da5a7e76237` |
| `approval.json` | `3826f72b14be72c17a847217b228c1301ddbc18bd1ac5105ee6264bf6b23d446` |
| `op-07-failed.json` | `cfeb3df52a257a990aad5a0b9d671225773db9b97f85a9765d58244ade514f43` |
| `deployment-status.json` | `d64281db4d3b7c1e33824e1d5d506b25c81a12ceb6458c10abf1207e0da22d8c` |
| `bootstrap-after.json` | `9847e944bd62799dfd86bb353a1389d7ebdc858051b47504407cdf98f69f16e5` |

This evidence qualifies rejection before Worker intake handoff on the exact
single-Host combination above. It does not qualify a successful Worker handoff,
draining an old Worker, retained-generation rollback, crash-boundary recovery,
Queue replacement, multi-host availability, EC2, ECS, Lambda, or SQS.
