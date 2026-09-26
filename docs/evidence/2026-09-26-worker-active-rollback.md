# Worker active verification and rollback on the disposable acceptance VM

Date: 2026-09-26

Issue: [#43](https://github.com/datashaman/provision/issues/43)

Target: `marlinf@provision-acceptance.local` (Incus VM restored from the clean snapshot before each scenario)

## Qualified combination

- Host: Ubuntu Server 26.04, `x86_64`
- systemd: `259 (259.5-0ubuntu3.4)`
- Podman: `5.7.0+ds2-3build1`
- RabbitMQ: `4.3.6`, image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`
- controller: macOS `arm64`, SHA-256 `ce09e91081ed706417939baba11c70a76acd4b05a53c1b3e548fe800f290ba24`
- restricted Linux executor: SHA-256 `3fb293c5dd0abfa7fd7b335c60e29b534a8165c6c63eb579039bc1c9953dd442`
- acceptance harness: [`scripts/test-worker-active-rollback-host.sh`](../../scripts/test-worker-active-rollback-host.sh), SHA-256 `85cc9cabf6fc547a4363fb374f899b6c1b6e742d99484cf88196553b7034323e`
- previous Worker: `provision-example-async-v1-2fcb2cec1d3d`, artifact `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`
- candidate Worker: `provision-example-async-v2-14caeac0dbda`, artifact `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95`

## Method

The automated loop independently restored the clean VM snapshot and recreated
the issue #40 Queue, Worker, Task, and Schedule baseline for each scenario. It
then installed the current restricted executor and ran the tracked harness
without an interactive prompt or clipboard relay.

Both approved Worker-only Plans pinned the candidate, previous Worker, unchanged
Queue generation, completed drain operation, and rollback policy. The
`verifyWorkerActive` operation recorded its exact Plan and operation digest
before publishing. It decrypted the existing systemd credential only in memory,
published one persistent message with broker confirmation, and joined Worker
processing and acknowledgement evidence by the same stable message ID. The
resolved Queue credential was absent from controller evidence.

## Scenario 1: healthy candidate

Plan: `sha256:5e486e7487c183dd3972a7f0cdac6cdf6237cfe01d719ccda6b261e6bd877af8`

- verification operation: `sha256:27908cd705eaeb7fc06c710cd953b95ac45d29f6abecb3f1921a6ab3c74626bc`
- message: `msg-ca01450bed6272dcb8bf31c3ea9eef09d73948aa970719bfdf756c6db97ae41d`
- RabbitMQ confirmed the message before the candidate's evidence was accepted;
- the candidate recorded both `processed` and `acknowledged` for that identity;
- the operation completed `succeeded` with status `healthy` and no rollback attempt;
- status committed v2 as active and v1 as the exact restartable previous Worker; and
- the Queue generation remained `provision-lab-messages-rabbitmq-4-3-6-34fc91a9de04`.

## Scenario 2: failed candidate and supported rollback

Plan: `sha256:381301385c65c156d80ab2b4c5e7b8662ad9452f7e81fcb61e307142ea7f85ef`

- verification operation: `sha256:27908cd705eaeb7fc06c710cd953b95ac45d29f6abecb3f1921a6ab3c74626bc`
- message: `msg-16513e4c024112fd88edb7c70a402382486831a6339e597812ba338b3e6d0bf1`
- the harness stopped v2 after intake activation and before active verification;
- RabbitMQ confirmed the stable verification message;
- the executor closed v2's gate, proved its unit stopped, and left it disabled;
- it re-observed the exact Queue, started v1 gated, reopened v1 intake, and
  required v1 to process and acknowledge that same message identity;
- the operation completed `failed` with status `rolled-back`, preserving the
  candidate failure reason while recording `rollbackAttempted=true` and
  `rollbackSucceeded=true`; and
- status reported v1 as active and the stopped, closed v2 as the candidate.

No result was rejected in either run. The journal distinguished a healthy
candidate from a known failed activation with proved rollback. The implementation
also defines a separate `uncertain` result: it cannot claim rollback success if
candidate fencing, Queue identity, prior-generation restartability, message
disposition, or durable Worker authority cannot be proved.

## Result and boundary

Both scenarios passed. This qualifies message-level Worker activation and the
supported single-Host rollback path for the exact combination above. The
guarantee is explicitly at least once: if a delivery is released during a
failure, redelivery retains its stable identity and duplicate-ledger evidence is
accepted as accounting, not presented as exactly once.

It does not qualify interruption recovery inside verification or rollback,
stale-executor recovery at those boundaries, rollback-window expiry cleanup,
Queue replacement, multi-host consumers, or a production-availability claim.
