# Managed Host Queue evidence — 2026-09-26

Issue #39 was exercised from a clean `provision-acceptance` VM snapshot using
the committed Queue implementation at `30d8066` and the final response-loss
and isolation harness at `fe94baf`. The seven-stage matrix completed with
`managed Queue acceptance passed`.

## Tested combination

| Property | Observed value |
| --- | --- |
| Controller | macOS `15.7.7`, Darwin `24.6.0`, `arm64` |
| Provision binary | `sha256:8b391a211f596dbc2b61579398ddaa0423958152f3ee5aab00e5f4106d131c4a` |
| Restricted executor | Linux `x86_64`, `sha256:b5e05925db42d53df82bade4fb51ba963746055c2e408ae3e648e04ae657d10c` |
| Host Target | Ubuntu Server `26.04`, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Management transport | strict SSH host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Authorization | authority key ID `sha256:5ded445e242a8c5aca90da032b444d10ec94935b07a29f81613ddc3ba0a2be95`; signed `prepareQueue` only |
| Runtime | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` |
| Plan | `sha256:ebbd7ad56ce07a1d8c6519d8791cf193a5ca6c3fc99bdddf529c5f02f1849a47`; one `prepareQueue` operation |

## Managed generation and topology

The logical Queue `provision-lab-messages` resolved to generation
`provision-lab-messages-rabbitmq-4-3-6-34fc91a9de04`. The exact healthy
observation reported one RabbitMQ node and one member for each durable quorum
Queue, with these owned broker resources:

- work, retry, and dead-letter quorum queues;
- direct work, retry, and dead-letter exchanges; and
- one exact binding from each exchange to its corresponding Queue.

The configured policy was a delivery limit of three, 24-hour work-message TTL,
ten-second retry delay, and seven-day dead-letter TTL. These values and topology
are qualified as exact configuration and observation. The runtime guarantees
reported by status remain limited to publisher confirms, manual
acknowledgement, and the exercised at-least-once path; rejection exhaustion,
dead-letter delivery, and TTL expiry were not claimed as exercised guarantees.

The acceptance probe was publisher-confirmed, observed available, consumed with
manual acknowledgement, and fully accounted as one accepted and one
acknowledged message.

## Recovery and idempotence

The first mutating SSH invocation completed remotely and its response was then
deliberately discarded by killing the controller process. After the abandoned
lease expired, `deployment resume` recorded a new intent with
`resumeOfAttemptId`, fencing token `8`, observed the already-created exact Queue
generation, and committed a successful outcome without recreating it.

The retained controller backend included earlier acceptance attempts; the final
clean-Host run is the fencing-token `7` intent followed by the token `8` resume
and successful outcome.

A second approved Plan used an independent State Backend and again observed the
same generation and status without duplicating any Queue, exchange, service,
container, path, or credential asset.

## Secret and host isolation boundaries

The Plan, approval, journal, status, and acceptance output contained the Secret
Reference but not the resolved AMQP credential. The credential was stored as a
user-scoped encrypted systemd credential, mounted read-only into the unit, and
materialized only in its runtime tmpfs path. systemd warned that this disposable
VM's credentials host key is on unencrypted media; this evidence therefore
proves encrypted delivery and plaintext non-disclosure, not resistance to full
host or disk compromise.

Before and after the Queue operation, the harness compared complete installed
package and system-unit inventories; unrelated active service states; content
hashes, ownership, modes, sizes, and paths for every unrelated Quadlet; and
unrelated rootless Podman containers, images, volumes, and networks. Every
comparison was byte-identical. Podman's netavark helpers were excluded from the
unrelated service-state comparison because their idle lifecycle belongs to the
Queue runtime rather than a deployment side effect. Exact RabbitMQ topology
inspection also rejected unrelated broker resources.

## Evidence identities

The retained controller evidence under `/tmp/provision-issue39-evidence` had
these SHA-256 digests at closeout:

| File | SHA-256 |
| --- | --- |
| `plan.json` | `0e2a7e374aedfbd56b5ef24d40225ffce8219f7cc1d2c17833532fdd51b9f103` |
| `approval.json` | `309089d2c11c33dc5a27e4f59e8a3c4602aadbc4a24f55c664097bf364bea17d` |
| `execution.json` | `789263914765be8920b62e1545241911e642751955c6e0bd4ac7e266931500ff` |
| `status.json` | `2aa9f1a9ff1d251cb1c742a326acbe39e297d7bf3b3b7ab1464e4dc652e59936` |
| `bootstrap-after.json` | `9712084ccc06fe1aed6c51358e2322737467bcac0e1bc5e171b135e0c51adf69` |
| `reexecution.json` | `478e4a9a88baa0fc465f2688cc99f1fac86959a4823f80deaa5129ea1aea2169` |
| `reexecution-status.json` | `727563a5f011ff109437d3aff92e931281d110aa35d1aa00f9ffe3c5c7767738` |

This evidence qualifies the first typed managed-Queue operation and status path
for the exact combination above. It does not qualify multi-node availability,
host-loss tolerance, Queue-generation replacement, Worker handoff, runtime
retry/dead-letter/expiry behavior, ECS, SQS, or any other Host or RabbitMQ
version.
