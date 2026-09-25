# Base host post-switch rollback evidence — 2026-09-25

Issue #10 was exercised from PR #25 on a freshly reset, disposable `base.local` host. The host ran Ubuntu Server 26.04 at `192.168.101.135` during the run.

## Bootstrapped authority

Bootstrap inspection reported `ready: true` with no findings:

- SSH ED25519 host fingerprint: `SHA256:0x1Qk4ka0jX4RaOC1n/KFBiHx60oQw0ycESpl0hTJpk`
- executor digest: `sha256:6509e669002c82713a7c13591b3e0515a749911a3a8890225b810a06b4413666`
- authority key ID: `sha256:8c9be3881f881d38a007448e254d438044ead1c0c7a4801b10993d4030f370fa`
- Caddy: `2.6.2`, active, valid, reachable, and configured for autosave resumption
- enabled operations: `stageArtifact`, `installGeneration`, `startCandidate`, `verifyCandidate`, `switchEndpoint`, and `verifyActive`

## Healthy baseline

The first Plan deployed the normal `v0.2.1` fixture and exercised the new post-switch operation on a healthy stable Endpoint:

- Plan: `sha256:55f2ad0db151e371d34bad02627b27c1509d717d50566eb6426f0efb2c727956`
- Artifact: `sha256:4e775436b605b9e7ea71c1bdc0941e3f5e345eab05ae264f6cf9fbe56afd6485`
- Generation: `provision-example-http-v0-2-1-4e775436b605`
- private port: `20087`
- stable port: `18080`

All six operations succeeded at fencing tokens 1 through 6. `op-06` observed successful liveness, readiness, and revision-bound verification through the stable port and reported `status: healthy`, `observedUpstream: 127.0.0.1:20087`, and `rollbackAttempted: false`.

## Reproducible post-switch failure

The fixture release [`v0.3.0`](https://github.com/datashaman/provision-example-http/releases/tag/v0.3.0) adds an opt-in acceptance mode. A Revision ending in `-fail-stable` remains healthy when checked directly on its private listener but returns HTTP 503 for health requests forwarded with the stable Endpoint Host header.

The second Plan deployed that fixture as a separate candidate:

- Plan: `sha256:05209f7c813b742d1a0e124f995d6840dc122cdd18ee2399946e342805ba5abc`
- Artifact: `sha256:b6f188a9b2f582a28618c361aa1b9550a5eb7935bcd39933bae2412487e14ef7`
- candidate Generation: `provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5`
- candidate private port: `26833`
- planned previous Generation: `provision-example-http-v0-2-1-4e775436b605` on private port `20087`

| Operation | Attempt | Fence | Outcome |
| --- | --- | ---: | --- |
| `op-01` stage Artifact | `attempt-3a257093df7cf51e0fd45c356a0ce412` | 7 | succeeded |
| `op-02` install Generation | `attempt-3e44e6b430c577a98b34371ea3b873ca` | 8 | succeeded |
| `op-03` start candidate | `attempt-78bbbf7b805d72a6b8a2c4fada6585a1` | 9 | succeeded |
| `op-04` verify candidate directly | `attempt-8e33df59b888bc423ee77a2664d87887` | 10 | succeeded, switch-eligible |
| `op-05` switch Endpoint | `attempt-370eb04b6cdee162bfa7fc33c8e5e0aa` | 11 | succeeded, candidate routed |
| `op-06` verify stable Endpoint | `attempt-884d22b275e63ab2b3ad3b130d4237fa` | 12 | failed, rolled back |

The candidate's direct `op-04` checks returned 204, 204, and 200. After the switch, its stable liveness check returned 503. The executor then:

1. kept both systemd units and release directories;
2. verified the exact signed previous Generation directly with 204, 204, and 200 responses;
3. changed only the Plan-owned Caddy route back to `127.0.0.1:20087`;
4. repeated the previous Generation's complete Health Contract through stable port `18080`, again receiving 204, 204, and 200;
5. atomically recorded the previous Generation as active and the failed candidate as retained previous material;
6. returned a failed operation with `status: rolled-back`, `rollbackAttempted: true`, `rollbackSucceeded: true`, the original `liveness check failed` reason, and the observed restored upstream.

The CLI exited nonzero with `post-switch verification failed; the previous Generation was restored`. This prevents dependent work from treating the failed deployment as successful.

## Durable outcome after Provision restart

A new CLI process opened the same SQLite State Backend and returned the complete append-only timeline. Journal sequence 23 retained the `op-06` intent and sequence 24 retained the failed, rolled-back typed observation with the active, previous, restored, health-check, and upstream evidence. There were no rejected results.

Independent host inspection then reported:

- active: `provision-example-http-v0-2-1-4e775436b605`, route-matched to `127.0.0.1:20087`;
- previous: `provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5`, still active on `127.0.0.1:26833` but not routed;
- stable `GET /verify`: `{"revision":"provision-example-http-v0-2-1"}`;
- direct failed-candidate `GET /verify`: `{"revision":"provision-example-http-v0-3-0-fail-stable"}`;
- both systemd units: active.

## Uncertain recovery coverage

The same typed path has deterministic tests for recovery that cannot be proved. They cover a failed stable check with no approved previous Generation, validation of structured uncertain observations, propagation to the State Backend as `uncertain`, a required recovery action, and persistence after reopening SQLite. The executor never reports success or a proved rollback in those cases.

## Boundary

This evidence qualifies post-switch ordinary-HTTP Health Contract verification, automatic rollback only after direct and stable proof of the exact retained previous Generation, retention of both Generations, and durable failed-but-recovered operator evidence.

It does not qualify role-specific drain completion, rollback-window cleanup, WebSocket recovery, or a full host reboot. The deliberately failed candidate remains running because cleanup and retention policy are later Plan operations.
