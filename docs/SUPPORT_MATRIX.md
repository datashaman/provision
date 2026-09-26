# Test-backed support matrix

This matrix records combinations exercised end to end. It is evidence of the stated guarantees for the exact combination, not a claim that every nearby OS or dependency version is supported.

## Direct-local HTTP host Deployment

| Provision build | Host OS | Architecture | systemd | Caddy | Result |
| --- | --- | --- | --- | --- | --- |
| commit `b4ee92789c242056b5fbe9a51ab42b8c5d02bc36`, binary `sha256:d4baa0bf57f3620ae1e473a748391eb55700679331267762087ad1e3f2c78229` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout and all five failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-direct-local-matrix.md). |
| commit `a2f7788b057c7e5afc686ec1a10e21d1528e01e2`, binary `sha256:f7ac2386c5dba47463cba4f46d9f1b7f83640656dc1aa223fe92625a5a6aea4a` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout, bounded ordinary-HTTP drain, lost-response recovery, and all failure classes passed in one repeatable seven-scenario run on `base`; see the [bounded-drain evidence](evidence/2026-09-25-base-host-bounded-http-drain.md). |
| commit `2090ea6a2887c5011fc3801915f19767bacab82a`, binary `sha256:1542523d7461c68893de71863cfec4b43eaa7a55a098646c3ddbdaac3f0acd92` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout, bounded drain, exact rollback-window recording, independent deadline verification, lost-response recovery for drain and rollback-window completion, and all failure classes passed in one repeatable seven-scenario run on `base`; see the [rollback-window evidence](evidence/2026-09-25-base-host-rollback-window.md). |
| commit `bcc9d3fb050512b432433822834b877eecaa6cfc`, binary `sha256:1542523d7461c68893de71863cfec4b43eaa7a55a098646c3ddbdaac3f0acd92` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | The complete lifecycle and all failure classes passed after restore from the clean `provision-acceptance` snapshot; the paired SSH run used the same image and engine seams. See the [complete lifecycle evidence](evidence/2026-09-25-acceptance-vm-complete-http-lifecycle.md). |

The matching host executor digests were `sha256:c532f3b78d291c400761b187c1fe8c14e84f1427b9d59498d71d1f73e6f0b21e` for the historical row, `sha256:af16725d2baa1b65cb5e38d1e24d8acc3f401d2992dde317e7431432ec8520de` for the bounded-drain row, and `sha256:a3dbdddb1b8a8c936a600219df66e983f988baa8ae834744c817645c7297964d` for both rollback-window rows. The harness emitted each row's values with `result: passed` in its structured support observation.

## Remote SSH HTTP host Deployment

| Provision build | Controller | Remote Host | SSH identity | Result |
| --- | --- | --- | --- | --- |
| commit `6598d23f0182d858713da1effb1462abee662b5c`, binary `sha256:caff13cd97adcfde3ea9b6573a59f8e601480cebd015f47c4520b7c1b4dfa349` | macOS, Darwin `24.6.0`, `arm64` | Ubuntu Server 26.04, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Caddy `2.6.2`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` | strict `known_hosts`; ED25519 `SHA256:E6M0uy67VlvnLBWjZhP+Ltk6v7wDS1yYo3eyqBOut6Y` | Healthy rollout plus all six remote failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-remote-ssh-matrix.md). |
| commit `bcc9d3fb050512b432433822834b877eecaa6cfc`, binary `sha256:5d1f3a67368fad6eab0ea22ee9324fe8c334c22c33f0cfd584dffd4d4e0efd01` | macOS, Darwin `24.6.0`, `arm64` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Caddy `2.6.2`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` | strict `known_hosts`; ED25519 `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` | The complete lifecycle, including bounded drain, rollback-window retention, lost responses, fresh-process resume, and all remote failure classes, passed in one seven-scenario run on `provision-acceptance`; see the [complete lifecycle evidence](evidence/2026-09-25-acceptance-vm-complete-http-lifecycle.md). |

The matching remote executor digests were `sha256:e9c910de10f89045c98d426454c60a3e7e467e3ed7c12c6994bda91bc93f6c38` for the historical row and `sha256:a3dbdddb1b8a8c936a600219df66e983f988baa8ae834744c817645c7297964d` for the complete-lifecycle row. The authority private key, approvals, and State Backend remained on the controller. The host received only observations and signed typed operations through a restricted one-shot executor.

## Managed RabbitMQ Host packaging

| Acceptance client | Host OS | Runtime identity | Service and topology | Result |
| --- | --- | --- | --- | --- |
| Linux `x86_64`, `sha256:6e5000d43a0b03014706d2cb08ec2a4babae01750dd3f31db7c59f9cbfd7ab73` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)` | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; image `docker.io/library/rabbitmq@sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` | Rootless Environment account `provision-lab`; user unit `provision-lab-rabbitmq.service`; node `rabbit@provision-lab-rabbitmq`; one durable member in quorum Queue `provision-issue36` | Exact manifest/config, service identity, Queue/node/inventory, encrypted credential delivery, publisher confirms, manual acknowledgements, stable-ID redelivery after unacknowledged close, automatic restart, and confirmed-message survival passed across a full Host reboot; see the [qualification](evidence/2026-09-25-rabbitmq-packaging-qualification.json) and [clean-restore controller evidence](evidence/2026-09-25-rabbitmq-acceptance-controller.json). |
| commit `30d8066`, macOS `arm64` binary `sha256:8b391a211f596dbc2b61579398ddaa0423958152f3ee5aab00e5f4106d131c4a`; harness `fe94baf` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)` | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; restricted executor `sha256:b5e05925db42d53df82bade4fb51ba963746055c2e408ae3e648e04ae657d10c`; same pinned image manifest | Logical Queue `provision-lab-messages`; exact work/retry/dead-letter quorum queues, exchanges, bindings, policy values, service, container, paths, encrypted credential, and records | Signed `prepareQueue`, publisher-confirmed accounting, lost-response observation and resume, independent re-execution without duplication, secret redaction, and unrelated Host inventory isolation passed from the clean VM snapshot; see the [managed Queue evidence](evidence/2026-09-26-managed-host-queue.md). |

The first row qualifies packaging; the second additionally qualifies the typed
managed Queue operation for the exact listed combination. Neither is a
production-availability claim. The one broker and one quorum member have zero
Host-failure tolerance. The image digest binds executable identity but does not
authorize automatic downgrade of persisted Queue data. Exact retry,
dead-letter, and retention configuration is observed, but rejection exhaustion,
dead-letter delivery, TTL expiry, ordering, and deduplication remain
unqualified runtime behavior. Alarm, feature, and health fields are observed
state rather than broader guarantees.

## First scheduled-message Host path

| Provision build | Host OS and runtime | Application generations | Result |
| --- | --- | --- | --- |
| macOS `arm64` binary `sha256:51b933ac55c4c86de10eb78f6bf6e983e83bf5ec22de71bcd0999745d6237e20`; Linux `x86_64` executor and schedule applet `sha256:bf891bd83aa47eeab8cbc3916b88a546e6ab8eeb5c0b09acb1671f51c4ad8ed8` | Ubuntu Server 26.04 VM, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at the qualified manifest | `provision-example-async` v0.1.0 Worker `sha256:4b6e79444cd9032facb5e027cafb7dca328d5f33eb334ccc3e83e70a30ce6e4a` and Task `sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5` | The approved thirteen-operation Plan installed and verified the immutable Task and gated Worker, opened Worker intake, installed the pinned nonresident Schedule runtime, activated the stable timer, recorded one occurrence before invocation, obtained publisher confirmation, and observed Worker processing and manual acknowledgement for the same message ID. Status reported each role separately. See the [live evidence](evidence/2026-09-26-first-scheduled-message.md). |

This row qualifies only the initial one-Worker, one-Task, one-Schedule path on
the listed single Host. It does not qualify Worker replacement, Schedule
handoff between revisions, retries or redelivery, overlap or missed-run
behavior, crash-boundary recovery, Queue replacement, or multi-host
availability.

## Guarantees exercised

- An unverified candidate cannot receive stable traffic.
- A failed Caddy switch does not silently become success; explicit recovery re-observes the host before continuing.
- A stable health failure restores traffic only after proving the exact retained previous Generation healthy both directly and through the stable Endpoint.
- A process interrupted after the traffic switch resumes from fresh host evidence without replaying the switch.
- After stable verification, both direct-local and remote-SSH native HTTP execution honor the complete declared Caddy handoff bound before stopping only the exact previous Generation; its unit definition and immutable Generation directory remain available.
- A lost successful drain response remains interrupted until resume proves durable host completion, then records `drained` without replaying the stop.
- Rollback-window retention binds the exact stopped previous Generation and all restart material to a deadline derived from the trusted switch time; it performs no cleanup.
- A lost successful retention response can be reconciled from the host's durable marker without shortening the window or replaying the mutation.
- A stale executor authorization cannot mutate the host or move the stable Endpoint.
- The durable journal distinguishes successful, failed, interrupted, resumed, and uncertain attempts.
- Only the expected Provision units and immutable Generation directories appear; Gimme-owned resources remain absent.
- Over SSH, losing a completed operation's response does not become success or cause blind replay. A new Provision process and a new one-shot remote executor reconcile the journal against fresh host evidence.
- Over SSH, a changed or unknown host key fails before the executor is invoked.
- An in-flight SSH disconnect and killed Artifact-stage executor produce an uncertain journal outcome. Resume removes only temporary files proven to belong to an earlier signed attempt from the same Environment before safely restaging.
- The initial Worker cannot consume until its immutable generation, process identity, Queue connection, Revision identity, and closed admission state are verified; status requires a durable exact active-generation record after intake opens.
- A stable timer invokes the digest-pinned nonresident Schedule runtime, which records the occurrence and stable Task Invocation before starting the exact generation-specific Task instance.
- The Task's publisher-confirmed message and the Worker's processing and manual acknowledgement evidence join on the same stable message identity.

## Limits

- The HTTP rows cover a single native `x86_64` HTTP component on one systemd-managed host, exercised both directly on the host and remotely from an `arm64` macOS controller over SSH. The RabbitMQ rows qualify only the exact rootless Podman/Quadlet packaging and Queue semantics stated above. The scheduled-message row qualifies only its exact initial systemd Worker, Task, and Schedule path. No row qualifies EC2, ECS, Lambda, databases, key-value stores, realtime services, asynchronous generation replacement, or multi-host asynchronous availability.
- The Endpoint guarantee covers ordinary HTTP requests. It does not promise WebSocket or other long-lived stream draining.
- Caddy configuration reload preserves the prior route on a rejected load. An external Caddy outage can still make the Endpoint unavailable until Caddy is restored.
- Remote mode adds a management-transport dependency, not a workload-availability dependency. If the controller cannot authenticate the host or cannot obtain enough fresh evidence after a lost connection, the operation remains interrupted or uncertain until an operator restores access and resumes it; stable traffic continues according to the last host state.
- SSH host-key rotation is deliberately not automatic. A reset or legitimate key change fails closed until the operator verifies and pins the replacement out of band.
- Candidate verification, Endpoint switching, stable verification, bounded rollback, bounded ordinary-HTTP drain completion, and rollback-window recording are implemented. The latest direct-local and remote-SSH rows support-qualify that complete sequence through the same engine seams. Historical rows predate parts of that sequence. Expiry-time cleanup is not implemented.
- A successfully drained previous Generation is stopped while its exact unit definition, immutable Generation directory and manifest, and digest-addressed Artifact remain installed and restartable. Other failed or older Generations may remain running because cleanup is not yet enabled.
- Host reset and bootstrap are separate operator-controlled procedures. The matrix does not claim unattended OS provisioning, upgrades, or in-place migration from Gimme.
- Versions not listed above require their own capability observation and acceptance run; they are not implied by this row.
