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
| macOS `arm64` binary `sha256:281e5aaecb93d8134d2b408866f448a1294d9392a5e25455cd13b714d9484128`; Linux `x86_64` executor and Environment-scoped schedule applet `sha256:a0392678ef0636e577e2a850e0491b49160b0ce17bd834e4db96fa7f8be46e87` | Ubuntu Server 26.04 VM, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at the qualified manifest | `provision-example-async` v0.1.1 Worker `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40` and Task `sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5` | The approved thirteen-operation Plan installed and verified the immutable Task and gated Worker, opened Worker intake, installed the pinned schedule runtime, activated the stable timer, recorded one occurrence before invocation, obtained publisher confirmation, and observed Worker processing plus broker-confirmed acknowledgement for the same message ID. Status reported each role separately. See the [live evidence](evidence/2026-09-26-first-scheduled-message.md). |

This row qualifies only the initial one-Worker, one-Task, one-Schedule path on
the listed single Host. It does not qualify Worker replacement, Schedule
handoff between revisions, retries or redelivery, overlap or missed-run
behavior, crash-boundary recovery, Queue replacement, or multi-host
availability.

## Failed Worker-candidate rejection

| Provision build | Host OS and runtime | Worker generations | Result |
| --- | --- | --- | --- |
| engine through commit `4405242`, macOS `arm64` binary `sha256:49560554d28a6c9639ca2361cc1c733d9a00757e7d363293673c3c4bbea04982`; Linux `x86_64` executor `sha256:a0f63e29bf5f8aa231f20fea7359b5f544093457bec68f8374bca60682010a1a`; harness `059071c` | Ubuntu Server 26.04 VM, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at the qualified manifest | active v0.1.1 Worker `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`; gated v0.2.0 candidate `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` | A separate immutable candidate started Queue-connected only after its root-owned gate and runtime state both proved intake closed. Complete event history showed two normal messages were claimed, processed, and acknowledged only by the active Worker. Deliberately stopping the candidate made verification fail with structured diagnostics; the exact candidate-only Plan authorized no handoff mutation, the candidate remained closed and actionable, and Queue, active Worker, retained Worker, Task, and Schedule identities stayed unchanged. See the [live evidence](evidence/2026-09-26-rejected-worker-candidate.md). |

This row qualifies only rejection before Worker intake handoff. It does not
qualify a successful handoff, old-Worker drain, retained-generation rollback,
or interruption recovery during a Worker replacement.

## Successful Worker intake handoff

| Provision build | Host OS and runtime | Worker generations | Result |
| --- | --- | --- | --- |
| macOS `arm64` binary `sha256:9d2998d2fb03ce64e5cac9920d7ddfbdbc7a6a3d246310672137d5e77d3db269`; Linux `x86_64` executor `sha256:7406a91908d260e226a6e380497c76e50ba1e7a7779fade0b4cecbd710899e34`; harness `85fb2a6` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at the qualified manifest | previous v0.1.1 Worker `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`; candidate v0.2.0 Worker `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` | Two independent clean-snapshot runs passed. The first observed the durable in-progress drain record, then let its exact held old-Worker delivery finish inside the bound. The second entered the reserved release phase, durably requeued the exact stable message ID before the final deadline, and had the candidate acknowledge that same ID. Both Plans bound activation and retention to the exact durable drain operation, Plan, completion deadline, and final deadline. In both runs the old Worker claimed no post-fence work, the Queue stayed unchanged, the candidate exclusively handled during/after messages, and the stopped old generation remained restartable through the 30-minute rollback window. See the [live evidence](evidence/2026-09-26-worker-intake-handoff.md). |

This row qualifies a completed replacement on one systemd Host with one managed
RabbitMQ consumer. It does not qualify interruption recovery inside the handoff,
rollback execution, expiry cleanup, multi-host consumers, or Queue replacement.

## Worker active verification and rollback

| Provision build | Host OS and runtime | Worker generations | Result |
| --- | --- | --- | --- |
| macOS `arm64` binary `sha256:ce09e91081ed706417939baba11c70a76acd4b05a53c1b3e548fe800f290ba24`; Linux `x86_64` executor `sha256:3fb293c5dd0abfa7fd7b335c60e29b534a8165c6c63eb579039bc1c9953dd442`; harness `sha256:85cc9cabf6fc547a4363fb374f899b6c1b6e742d99484cf88196553b7034323e` | Ubuntu Server 26.04 VM, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at the qualified manifest | previous v0.1.1 Worker `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`; candidate v0.2.0 Worker `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` | Two independent clean-snapshot runs passed. A healthy candidate became authoritative only after acknowledging a publisher-confirmed stable message. A candidate stopped after activation was first fenced and disabled; the exact previous generation was restored and directly proved by processing and acknowledging the same message identity. The Queue remained unchanged, status committed exact active/previous/candidate identities, and the journal distinguished `healthy`, `rolled-back`, and the separately validated `uncertain` contract. See the [live evidence](evidence/2026-09-26-worker-active-rollback.md). |

This row qualifies message-level active verification and supported automatic
rollback on one systemd Host with one managed RabbitMQ consumer. It does not
qualify interruption recovery during those steps, expiry cleanup, multi-host
consumers, or Queue replacement.

## Worker handoff interruption recovery

| Provision build | Host OS and runtime | Worker generations | Result |
| --- | --- | --- | --- |
| macOS `arm64` binary `sha256:073ed6e1ae306918f0cd04c66fa568dcba1797c59c5e6ddde35e3231cd7177ca`; Linux `x86_64` executor `sha256:65c0fa485caa08d1ef39971a8765618db535518a216a24e5248770dfd013b71f`; harness `sha256:69abf351114b4861be34537d5e5fed53581bed0c2ace348660f0e4a5e82873b4` | Ubuntu Server 26.04 VM, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at the qualified manifest | previous v0.1.1 Worker `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40`; candidate v0.2.0 Worker `sha256:14caeac0dbdaff68a2644798b0a1b2549f82342bedbf79d9b8da4625e2080d95` | Two independent clean-snapshot runs passed. For fence, drain/release, activation, active verification, and retention, the controller was killed once before Host dispatch and again after durable Host completion. A third process acquired a higher fence, observed before acting, and committed each completed mutation without replay. The healthy run ended at fence 33. The rollback run reconstructed the exact known `failed`/`rolled-back` result after its response was lost. Queue identity and exact Artifact, unit, active, candidate, previous, and retained-generation inventories matched with no abandoned attempt material. See the [live evidence](evidence/2026-09-26-worker-handoff-recovery.md). |

This row qualifies controller-process interruption and lost-response recovery
across one single-Host Worker replacement. Ambiguous publisher confirmation or
conflicting Queue, gate, unit, message, or authority evidence still pauses. It
does not qualify Host/data loss, retention expiry cleanup, multi-host consumers,
or Queue replacement.

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
- A replacement Worker starts under a separate immutable unit only when its root-owned gate and runtime state both prove intake closed. Failed verification leaves every downstream handoff operation blocked by its exact dependency, keeps active Worker and Queue ownership unchanged, and reports the failed checks plus a recovery action without disclosing the Queue credential.
- A verified replacement Worker remains gated while the exact old Worker closes intake. The old Worker receives no new messages after the fence, may finish its held delivery within the completion allowance, or enters a reserved stop-and-release phase that durably requeues it under the same stable identity before the final declared deadline. Candidate intake opens only after the old unit is stopped with zero in-flight deliveries; the Queue generation remains unchanged and the stopped old generation stays restartable through the recorded rollback deadline.
- The Worker drain records one Plan-bound operation identity, completion deadline, final deadline, release start, and completion before reporting success. Resume reuses that bound, activation and retention require its exact Plan and operation digest, and a stale Plan, stale active Worker, changed Queue generation, or settlement completed after the deadline fails closed.
- Active Worker verification publishes a persistent, publisher-confirmed, stable-identity message and commits candidate authority only after matching processing and acknowledgement evidence. A failed candidate is fenced before the retained previous Worker reopens intake; rollback succeeds only after that exact generation accounts for the same message. Unprovable Queue, fence, generation, message, or authority evidence remains explicitly uncertain, and redelivery is described as at least once rather than exactly once.
- Worker handoff resume takes a higher fence and re-observes Queue, gates, units, message settlement, durable active authority, and the operation-specific drain, verification, or retention record before deciding satisfied, pending, or uncertain. Completed mutations with lost responses are journaled without replay; a completed rollback remains a known failed candidate outcome rather than being rewritten as success. Lower-fence late results are rejected and retained only as diagnostic evidence.
- A stable timer invokes the digest-pinned nonresident Schedule runtime, which records the occurrence and stable Task Invocation before starting the exact generation-specific Task instance.
- The Task's publisher-confirmed message and the Worker's processing and manual acknowledgement evidence join on the same stable message identity.

## Limits

- The HTTP rows cover a single native `x86_64` HTTP component on one systemd-managed host, exercised both directly on the host and remotely from an `arm64` macOS controller over SSH. The RabbitMQ rows qualify only the exact rootless Podman/Quadlet packaging and Queue semantics stated above. The scheduled-message row qualifies only its exact initial systemd Worker, Task, and Schedule path. The Worker rows qualify pre-handoff rejection, completed replacement, message-level activation, supported rollback, and controller-interruption recovery only for the stated single-host examples. No row qualifies EC2, ECS, Lambda, databases, key-value stores, realtime services, Schedule replacement, Queue replacement, Host/data-loss recovery, or multi-host asynchronous availability.
- The Endpoint guarantee covers ordinary HTTP requests. It does not promise WebSocket or other long-lived stream draining.
- Caddy configuration reload preserves the prior route on a rejected load. An external Caddy outage can still make the Endpoint unavailable until Caddy is restored.
- Remote mode adds a management-transport dependency, not a workload-availability dependency. If the controller cannot authenticate the host or cannot obtain enough fresh evidence after a lost connection, the operation remains interrupted or uncertain until an operator restores access and resumes it; stable traffic continues according to the last host state.
- SSH host-key rotation is deliberately not automatic. A reset or legitimate key change fails closed until the operator verifies and pins the replacement out of band.
- Candidate verification, Endpoint switching, stable verification, bounded rollback, bounded ordinary-HTTP drain completion, and rollback-window recording are implemented. The latest direct-local and remote-SSH rows support-qualify that complete sequence through the same engine seams. Historical rows predate parts of that sequence. Expiry-time cleanup is not implemented.
- A successfully drained previous Generation is stopped while its exact unit definition, immutable Generation directory and manifest, and digest-addressed Artifact remain installed and restartable. Other failed or older Generations may remain running because cleanup is not yet enabled.
- Host reset and bootstrap are separate operator-controlled procedures. The matrix does not claim unattended OS provisioning, upgrades, or in-place migration from Gimme.
- Versions not listed above require their own capability observation and acceptance run; they are not implied by this row.
