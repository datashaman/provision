# Worker intake handoff on the disposable acceptance VM

Date: 2026-09-26

Issue: [#42](https://github.com/datashaman/provision/issues/42)

Target: `marlinf@provision-acceptance.local` (Incus VM restored from the clean snapshot before each scenario)

## Qualified combination

- Host: Ubuntu Server 26.04, kernel `7.0.0-34-generic`, `x86_64`
- systemd: `259 (259.5-0ubuntu3.4)`
- Podman: `5.7.0+ds2-3build1`
- RabbitMQ: `4.3.6`, image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`
- controller: macOS `arm64`, SHA-256 `9d2998d2fb03ce64e5cac9920d7ddfbdbc7a6a3d246310672137d5e77d3db269`
- restricted Linux executor: SHA-256 `7406a91908d260e226a6e380497c76e50ba1e7a7779fade0b4cecbd710899e34`
- acceptance harness: [`scripts/test-worker-intake-handoff-host.sh`](../../scripts/test-worker-intake-handoff-host.sh), SHA-256 `85fb2a6b21f21113ab75d8524a14ce7d27b2f48550f78a4874de63092f727758`
- previous Worker: `provision-example-async-v1-2fcb2cec1d3d`, artifact `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`
- candidate Worker: `provision-example-async-v2-14caeac0dbda`, artifact `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95`

## Method

The automated loop restored the VM's clean snapshot and recreated the qualified
issue #40 Queue, Worker, Task, and Schedule baseline independently for each
scenario. It then installed the current restricted executor and ran the tracked
harness without an interactive prompt or password relay.

Both approved Plans contained exactly:

1. `prepareQueue`
2. `stageArtifact`
3. `installWorkerGeneration`
4. `startWorkerCandidate`
5. `verifyWorkerCandidate`
6. `fenceWorkerIntake`
7. `drainWorkerPrevious`
8. `activateWorkerIntake`
9. `verifyWorkerActive`
10. `retainWorkerPrevious`

The five handoff operations formed a strict dependency chain. Their signed
inputs bound both Worker generations, Queue generation
`provision-lab-messages-rabbitmq-4-3-6-34fc91a9de04`, the 30-second drain bound,
and the 30-minute rollback window. Activation, active verification, and
retention also named the exact drain-operation digest. The executor recorded
the Plan ID, operation digest, start, completion deadline, and final deadline
before waiting, so a retry cannot silently obtain a fresh drain bound. The
declared maximum reserves a deterministic final stop-and-release phase, and a
successful observation proves its recorded completion did not exceed the final
deadline.

Four deterministic messages were publisher-confirmed in each run: before the
fence, held in flight at the fence, accepted while old intake was closed, and
accepted after candidate activation. Evidence was joined by stable message ID,
not by timing or log order.

## Scenario 1: in-flight completion inside the bound

Plan: `sha256:72986ed9de59d51f1a5d9f65ab7e8f0ad35a89163a113c434ea07b5999fc7fe2`

- the old Worker held message `msg-2159b977f5532640aece10db809be6d869194fd5829ce896a4902e3f49bb3cda` when its intake fence became durable;
- the harness first observed that exact identity in the durable in-progress drain record, then released it so the old Worker could process and acknowledge it;
- drain completed as `previous-drained` with `boundElapsed=false`;
- a message accepted after the fence had no old-Worker event and was later acknowledged by the candidate;
- a post-activation message was also acknowledged only by the candidate; and
- drain operation `sha256:d1b4d42deb4cf85a7376132e1a18a1dfd3217c9385ffce8d309ff2442de3cffc` started at `2026-09-26T10:40:57.27100326Z`, completed at `2026-09-26T10:40:59.852909017Z`, and retained its final `2026-09-26T10:41:27.27100326Z` deadline; and
- the old generation was recorded restartable from `2026-09-26T10:42:41.538993998Z` through `2026-09-26T11:12:41.538993998Z`.

## Scenario 2: deadline release and candidate completion

Plan: `sha256:b5d9f9374525691bc70ab7a1d1f3d750354a9d386b8329f5cff170d16d306ee9`

- the old Worker held message `msg-43a7f0d298052b001bd4d2527df93d6626033075c62a2bcf94840c817273f477` when its intake fence became durable;
- at the recorded completion deadline `2026-09-26T10:51:20.869482587Z`, the executor entered the reserved release phase at `2026-09-26T10:51:20.925281987Z`; the old Worker recorded `requeued` and stopped with zero in-flight deliveries;
- the durable drain observation was `previous-released`, with `boundElapsed=true`, and named that exact value as both `inFlightMessageId` and `releasedMessageId`;
- after candidate activation, the candidate processed and acknowledged the same stable message ID;
- messages accepted during and after the fence were acknowledged only by the candidate; and
- settlement completed at `2026-09-26T10:51:21.625747366Z`, before the exact final `2026-09-26T10:51:30.869482587Z` deadline; and
- the old generation was recorded restartable from `2026-09-26T10:52:57.676893145Z` through `2026-09-26T11:22:57.676893145Z`.

In both runs the Queue generation and all of its recorded identities were
unchanged, every handoff journal outcome was `succeeded`, no result was rejected,
and the resolved Queue credential was absent from durable controller evidence.

## Result and boundary

Both scenarios passed. This qualifies successful single-Host Worker replacement
for the exact combination above: closed candidate admission, durable old-consumer
fencing, bounded completion or stable-ID release, candidate-only new work, and
retention of the stopped previous generation.

It does not qualify interruption recovery inside the Worker fence/drain/cutover
sequence, rollback execution, retention expiry cleanup, Queue replacement,
Schedule replacement, multi-host consumers, or a production-availability claim.
