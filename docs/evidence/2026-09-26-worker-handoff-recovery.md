# Worker handoff interruption recovery on the disposable acceptance VM

Date: 2026-09-26

Issue: [#44](https://github.com/datashaman/provision/issues/44)

Target: `marlinf@provision-acceptance.local` (Incus VM restored from the clean snapshot before each scenario)

## Qualified combination

- Host: Ubuntu Server 26.04, `x86_64`
- systemd: `259 (259.5-0ubuntu3.4)`
- Podman: `5.7.0+ds2-3build1`
- RabbitMQ: `4.3.6`, image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`
- controller: macOS `arm64`, SHA-256 `073ed6e1ae306918f0cd04c66fa568dcba1797c59c5e6ddde35e3231cd7177ca`
- restricted Linux executor: SHA-256 `65c0fa485caa08d1ef39971a8765618db535518a216a24e5248770dfd013b71f`
- acceptance harness: [`scripts/test-worker-handoff-recovery-host.sh`](../../scripts/test-worker-handoff-recovery-host.sh), SHA-256 `69abf351114b4861be34537d5e5fed53581bed0c2ace348660f0e4a5e82873b4`
- previous Worker: `provision-example-async-v1-2fcb2cec1d3d`, artifact `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`
- candidate Worker: `provision-example-async-v2-14caeac0dbda`, artifact `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95`

## Method

The automated loop independently restored the clean VM and recreated the issue
#40 asynchronous baseline for each scenario. At each externally visible Worker
boundary, the harness killed one controller before Host dispatch. It then
expired only that disposable controller lease, resumed under a higher fencing
token, let the Host complete the mutation, withheld the result, and killed that
controller too. A third process acquired another higher fence and reconciled
from a fresh Host observation. The normal two-minute signed authorization
lifetime remained in force; only the local test State Backend's abandoned lease
was advanced to permit the deterministic takeover.

The exercised boundaries were:

1. old-consumer intake fence;
2. bounded drain or stable-identity release;
3. candidate intake activation;
4. post-activation message verification, including active authority; and
5. previous-generation rollback-window retention.

The healthy Plan was
`sha256:12aa70882d30cd60c5f459b0842d3432786f96d0d358c42199928a3e2640b410`.
Its boundary attempts used fences `19` through `33`: each operation had exactly
three strictly increasing intent fences and one final authoritative outcome.
The active-verification result was `succeeded`/`healthy` for stable message
`msg-619bb46957809c7fce03fca3b74ad4a37fb849ffad8d2de36d81de332402bfe1`.

The rollback Plan was
`sha256:08aa7e692841bb49a7c862aa02ab04066b41bd055226a07a674961da6e5c7a69`.
After candidate activation the harness stopped its exact unit. Verification
fenced the candidate, restored the retained previous Worker, and required it to
process and acknowledge stable message
`msg-11393cc14014d1c5929ae521afb83cbf576c55385c0c58580c372c01e4e652ce`.
The response was withheld after Host completion. Fresh observation reconstructed
the exact `failed`/`rolled-back` result at fence `30`; it did not replay rollback
and did not relabel the failed candidate as a successful deployment.

## Fencing and ambiguity

Every resumed intent records the immediately preceding `resumeOfAttemptId`.
Deterministic State Backend coverage also submits a late success from the lower
fence after a higher-fenced failed/rolled-back result is authoritative. The late
result is rejected, cannot change journal or Environment state, and remains
queryable in `rejectedResults` with its submitted observation.

A verification record with publisher-confirmed identity can continue from its
message and rollback checkpoints without republishing. A record that says
publish started but lacks durable publisher confirmation is deliberately
ambiguous: resume fences the candidate and reports the exact Queue/message/unit
evidence and recovery action rather than guessing whether to publish again.

## Inventory and boundary

Both runs preserved the exact managed Queue generation and its owned topology.
Final inventory contained only the baseline and approved candidate Artifact,
unit, and immutable generation additions. The healthy run recorded the candidate
as active and the exact previous Worker as restartable. The rollback run recorded
the previous Worker active and the exact stopped candidate closed. No abandoned
attempt Artifact, unit, Queue, or generation material appeared.

This qualifies controller-process interruption, lost-response reconciliation,
known rollback reconstruction, and stale-result fencing for the stated
single-Host Worker path. It does not qualify Host or RabbitMQ data loss,
multi-host consumers, Queue replacement, retention expiry cleanup, or an
exactly-once processing claim.
