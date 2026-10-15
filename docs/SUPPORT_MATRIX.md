# Test-backed support matrix

This matrix records combinations exercised end to end. It is evidence of the stated guarantees for the exact combination, not a claim that every nearby OS or dependency version is supported.

## Independently approved agent Artifact staging

| Source and proof | Controller and authority | Remote Host and operation | Result |
| --- | --- | --- | --- |
| Reviewed source `529ee57e176f46695271e4303c95ac9230b35838`, merged through [PR #131](https://github.com/datashaman/provision/pull/131); [live proof and binary identities](evidence/2026-10-15-mediated-stage-artifact.md) | Ubuntu 26.04.1 `amd64`; isolated `requestbot`/`requestother` accounts, separate trusted `authority` account; filesystem inbox, private SQLite state and credentials | Separate Ubuntu 26.04 `amd64` VM over strict SSH; native HTTP `stageArtifact` only | All 15 case groups and 117 command exit assertions passed: requester confinement, exact request/Plan/operation approval, denied bypass, matching cached digest and receipts, expiry, deduplication, and fenced recovery including two requests sharing one Plan. |

Both VMs shared physical hardware and a LAN. The root test supervisor exercised
the human approval entry point under the trusted account; the proof does not
establish physical human presence or protect a compromised trusted operator or
administrator. It does not qualify local mediated Hosts, other OS combinations,
other operations, installation/activation, executable safety, hosted authority,
or automated approval. Broader single-operator rows below have a separate trust
boundary. See the [first-release checklist](first-release-checklist.md).

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

## Managed Valkey Host packaging

| Acceptance client | Host OS | Runtime identity | Service and topology | Result |
| --- | --- | --- | --- | --- |
| proof harness `sha256:7d5046bb52931f3a7d2fa46bfd56c9ec27f5376c0e5aa2be7377a9d7e05c38bb`; controller harness `sha256:9bc43ff55ef8992da07852c3825612cdb8a7af7075e81eb9da1ae2fe60592d05`; approval records `sha256:40b13cc8ad416c5cb35efb653c1b594dff3c7ad1f0d24e0dab90f21d21654d9c` (`9.1.2`), `sha256:01bd451323b78cf199abdf11ba0d792d044c6ae662880574aaaf9ed34d946913` (`9.1.1`) | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)` | Podman `5.7.0+ds2-3build1`; Valkey `9.1.2` image `docker.io/valkey/valkey@sha256:ac9c858b86dc81f3482d53a9d854547379a5f41e012500f839ab257c399063e0` (index `sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd`, qualification `sha256:76f79d213d3cc181a6868a7879fa3133f2bdd3affa01542a54beb43c2fff3769`); Valkey `9.1.1` image `docker.io/valkey/valkey@sha256:cc03bc07761acc552c960bfbcc66196dcf02e949dadb92d941d970ed6c782fea` (index `sha256:64e361b630ecf18dff7ca4df6a88e6eafc193687eb48cff2c7e0a293ab67d29a`, qualification `sha256:01112f9103a64f8b4a2b2e30835552895f27d29e9841111d2b0808ef660ba9bd`) | Rootless Environment account `provision-lab`; user unit `provision-lab-valkey.service`; container `provision-lab-valkey`; generations `valkey-9-1-2-ac9c858b86dc` and `valkey-9-1-1-cc03bc07761a`; loopback listener `127.0.0.1:26379` | Exact digest/config, loopback-only listener, authenticated client connectivity, AOF `appendonly yes` with `appendfsync everysec`, non-secret credential reference, owned generation data path, and keys surviving a full Host reboot passed for both images from a clean disposable VM snapshot; the VM restored clean afterward. See [ADR 0076](adr/0076-select-managed-host-valkey-packaging.md) and the [packaging evidence](evidence/2026-10-10-valkey-packaging-comparison.md). |

These rows qualify the packaging choice for the first managed Host-local
key-value store tracer, authoritative Store Data Role only. They advertise no
Store Transition, replication, write fence, or rollback guarantee, and exclude
backup, restore, host-loss recovery and crash durability beyond a clean reboot,
derived or ephemeral roles, multi-node Valkey, ElastiCache, external Valkey, and
other Valkey/image/Podman/OS versions. The credential proof covers systemd
encrypted credential delivery scoped to the Environment user unit; it does not
assert that the VM's backing media is encrypted.

## Managed Valkey first generation and HTTP binding

| Acceptance client | Host OS | Runtime identity | Service and topology | Result |
| --- | --- | --- | --- | --- |
| direct-local harness `sha256:25dc50db91b5de7f156cb4b6c98507e9b3a47b4607bd6bdecfccd9ebcb355958`; HTTP harness `sha256:a4bb04ab14b0be49b61e046f937315258ad0ab777594430ee3086be9a079372b`; controller `sha256:40a27b879ce058adef3cecc04bf47d0085ffc19f9150ff25dbb2140a8fbafaee`; fixture workload source `sha256:933bb6b62ae83b4dc066be70e5d44ff57f344257cb580d21bdb016e1a98b068b`; built on top of `1018b9f0b2cc67198681e65feac23edec8c2c470` | Ubuntu Server 26.04 VM (`provision-acceptance`, clean snapshot restored before each mode), direct-local Host Handler | Valkey `9.1.2` digest-pinned image, rootless Podman/Quadlet, AOF `appendonly yes` `appendfsync everysec`, loopback listener `127.0.0.1:26379` | one authoritative Key-Value Store generation; `prepareKeyValue` executed through the restricted executor, with `none`, `before` and `after` interruption/resume modes; plus a Key-Value-bound HTTP workload behind the stable Caddy Endpoint | passed: inspection reports exact version, packaging identity, persistence and listener; authenticated client round trip and deterministic key read-back before the generation is reported active; the workload binds by logical reference and proves the same keys through its candidate verification; the resolved credential appears in no Plan, journal, status or evidence |

This row qualifies only the exact combination above (Valkey `9.1.2`; not `9.1.1`
live-executed in this slice). It advertises no Store Transition, replication,
write fence, or rollback guarantee, and excludes backup, restore, host-loss recovery, derived or
ephemeral roles, multi-node Valkey, ElastiCache and external Valkey. Evidence is kept under the
git-ignored `work/issue-97-live/evidence`.

## Managed Valkey first generation: strict-SSH parity

| Acceptance client | Host OS | Runtime identity | Service and topology | Result |
| --- | --- | --- | --- | --- |
| harness `sha256:3dbfe23f4be58aee2883138f15924fc0c16a80a008cb61b2b056555d179806a5`; controller `sha256:18e66f0708de23ada99b80cc7b09051c146fba4db535fce5ef8ac03bdb2111f6`; direct-local controller `sha256:e2dc5f6cfa18f225bbbb6dceb18c299bcc59c1d68898469bae498796c022c25a`; Provision controller binary built on macOS, executor `sha256:640de6d71e8e52859aa7532a1d237a0dc3d0239fbae2120f9a4b3010dfeccf59` | Ubuntu Server 26.04 VM `provision-acceptance` (Linux 7.0.0-34-generic), clean snapshot restored before every mode | Valkey `9.1.2` image `sha256:ac9c858b86dc81f3482d53a9d854547379a5f41e012500f839ab257c399063e0`, AOF `appendonly yes` `appendfsync everysec`, listener `127.0.0.1:26379`, Podman `5.7.0+ds2-3build1` | one authoritative Key-Value Store generation; Provision, approval state and signing key on the controller, Host Target reached only through strict SSH with a pinned host key | passed: modes `none`, `before`, `after`, `undecidable` on direct-local and strict SSH with identical accounting (2 deterministic keys read back, no resolved credential in evidence); `undecidable` leaves the unreadable-record resume unreplayed and recorded uncertain, then recovers |

The `undecidable` mode makes the generation record unreadable after an
interrupted, completed mutation; resume must observe, refuse to replay, and
record an uncertain outcome. Re-planning an exactly active Key-Value-bound
workload now emits only the idempotent store preparation, and an active
Key-Value Store generation that differs from the plan fails closed as an
unqualified Store Transition. Exclusions are unchanged from the row above.
Evidence is under the git-ignored `work/issue-97-live/parity-evidence`.


## Managed Valkey Host transition contract

| Test seam | Qualified input | Operation contract | Result |
| --- | --- | --- | --- |
| Go planner, restricted-executor, and local result-verifier tests for issue #99 | Existing healthy authoritative managed Valkey generation (`9.1.1` or `9.1.2`, AOF `everysec`) with a registered same-major target image | `prepareKeyValueCandidate`, `synchronizeKeyValueCandidate`, `fenceKeyValueWrites`, `switchKeyValueAuthority`, `verifyKeyValueActive`, and `retainKeyValuePrevious` | Plan preview exposes forward cutover classified `lossless-after-bounded-write-fence` (ACL write revocation plus client kill, `30s` maximum, replication offset equality carried in signed results), keeps rollback `forward-only`, uses a separate candidate service/container/listener, rejects unregistered, unhealthy, non-AOF, non-authoritative, or cross-major active generations, rejects unsafe observed candidates, requires phase-specific signed results, and refuses to switch authority without the Plan's own fence record, a rejected probe write, and equal offsets over an up link. Stale executor attempts are rejected by the host fence before any Key-Value Store command. See [ADR 0077](adr/0077-qualify-forward-only-host-valkey-transition-contract.md). |
| Direct-local failure matrix on the disposable VM (#100) | CLI-provisioned `9.1.1` retargeted at `9.1.2` | Every transition operation interrupted `before` and `after`; `undecidable` at the authority switch | Each mode recovers by observing the Host first without replaying a data-bearing step; `undecidable` records an `uncertain` outcome with reason and recovery action and does not restart the authoritative container. State accounting is identical across modes. See [evidence](evidence/2026-10-11-valkey-direct-local-failure-matrix.md). |
| Strict-SSH parity on the disposable VM (#101) | Same as #100, from the controller over pinned SSH | The #100 matrix repeated over SSH in `before`, `after` and `undecidable` modes | Transport loss around every operation recovers by observing the remote Host first; accounting matches direct-local in all three modes (replication offsets at the fence compared for equality, not value). See [evidence](evidence/2026-10-11-valkey-ssh-parity.md), including an open intermittent failure at the authority switch. |
| Direct-local candidate-failure and forward-only boundaries on the disposable VM (#102) | CLI-provisioned `9.1.1` retargeted at `9.1.2` | Candidate stopped after `prepareKeyValueCandidate`; full transition followed by a write on the new generation | The failed synchronization ends `failed`; the active generation, its data and the workload keys are unchanged, no authority-switch evidence exists and a direct switch is refused. After cutover a write accepted by `9.1.2` is absent from the retained `9.1.1` data and rollback stays `forward-only`. Excludes backup/restore, host-loss recovery, derived/ephemeral roles, multi-node Valkey, ElastiCache, external Valkey, and any Valkey, image, Podman or OS version other than the tested one. See [evidence](evidence/2026-10-13-valkey-boundaries.md). |

The first row qualifies the contract shape and executor guardrails. The live rows qualify only the
exact combination they list (Valkey `9.1.1` to `9.1.2`, AOF `everysec`, Ubuntu 26.04 VM, rootless
Podman) and none qualifies crash durability, host-loss recovery,
backup/restore, derived or ephemeral roles, multi-node Valkey, ElastiCache, external Valkey,
cross-major upgrades, an HTTP-bound transition, or any rollback claim.

## Managed PostgreSQL Host packaging

| Acceptance client | Host OS | Runtime identity | Service and topology | Result |
| --- | --- | --- | --- | --- |
| proof harness `sha256:b1fd8d285ba3ab14a6b906eecc0f6f436f4ed97b46a0fb472d056bf402d051f3`; controller harness `sha256:e578c193fed177289933f7da803df8db046004c03d8e435ef9fe5dfae278ee70`; approval record `sha256:4addc32b3819a23d4fc16a6ce13128b1715baee047eca22d1092fb0168e797fe` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)` | Podman `5.7.0+ds2-3build1`; PostgreSQL `17.6`; image `docker.io/library/postgres@sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409`; image index `sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929` | Rootless Environment account `provision-lab`; user unit `provision-lab-postgresql.service`; container `provision-lab-postgresql`; generation `postgresql-17-6-b86568d3e0fe`; owned data path `/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-17-6-b86568d3e0fe/data`; loopback listener `127.0.0.1:25432` | Exact digest/config, service identity, loopback-only listener, SQL connectivity, non-secret credential reference, owned generation data path, automatic restart after full Host reboot, and durable row survival passed from a clean disposable VM snapshot. The controller also proved the VM restored clean afterward. See the [packaging evidence](evidence/2026-09-28-postgresql-packaging-comparison.md). |

This row qualifies the packaging choice for the first managed Host-local
Database tracer. It is not a production Database lifecycle claim. It does not
qualify backups, restores, host-loss recovery, multi-node availability,
automatic rollback after candidate writes, external PostgreSQL, RDS, or other
PostgreSQL/image/Podman/OS versions. The
credential proof covers systemd encrypted credential delivery scoped to the
Environment user unit; it does not assert that the VM's backing media is
encrypted.

## Managed PostgreSQL second image and CLI-provisioned transition

| Proof harness and approval | Host | Image | Result |
| --- | --- | --- | --- |
| proof harness `scripts/prove-postgresql-quadlet-host.sh`; approval record `sha256:d63b77a7a024bcc3ea8bbd73650a209a68140801c257c8fdd099388525ed1b6d` | Ubuntu Server 26.04 VM, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Podman `5.7.0+ds2-3build1` | PostgreSQL `17.7`; image `docker.io/library/postgres@sha256:030da09481c3876b71a7e49738a932e1c18c398201a1e4ccfdbff1e5a541215b`; image index `sha256:2006493727bd5277eece187319af1ef82b4cf82cf4fc1ed00da0775b646ac2a4`; generation `postgresql-17-7-030da09481c3`; qualification digest `sha256:0b786851c1ab07c183650c91dd3ad8bbbe8c74c11e9ba535d585eb0841718ad0` | Same packaging proof as `17.6` (service, loopback listener, SQL connectivity, owned data path, encrypted credential, restart and durable data across a full Host reboot) passed from the clean snapshot, and the VM restored clean afterward. See the [qualification](evidence/2026-09-29-postgresql-17-7-packaging-qualification.json) and [controller evidence](evidence/2026-09-29-postgresql-17-7-acceptance-controller.json). |
| `scripts/test-direct-local-postgresql-host.sh` under `--fault-mode before`, `after`, and `undecidable`, via `scripts/run-direct-local-postgresql-acceptance.sh` | Same VM, direct-local | `17.6` provisioned through the CLI, then transitioned to `17.7` by declaring `version: "17.7"`; same-major only | No out-of-band state seeding. Backup, isolated restore, forward-only Store Transition, previous-generation retention, and interruption recovery passed in all three modes. In `undecidable` mode the authority-switch evidence was made unreadable after the switch landed; resume recorded an uncertain outcome with reason and recovery action, did not replay the switch, and completed after the evidence was restored. See the [transition evidence](evidence/2026-09-29-postgresql-second-image-transition.md). |
| `scripts/test-remote-ssh-postgresql-host.sh` under `--fault-mode before`, `after`, and `undecidable`, via `scripts/run-remote-ssh-postgresql-acceptance.sh` | Same VM, strict-SSH from an `arm64` macOS (Darwin 24.6.0) controller; ED25519 host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` pinned in `known_hosts`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` | Same `17.6` to `17.7` transition on Podman `5.7.0+ds2-3build1`, systemd `259 (259.5-0ubuntu3.4)`, executor `sha256:50b71f5495b66b73b83eaf500b589732cd22e77a48f1d354d097c3e49fbfb2fe`, one PostgreSQL node | Same configuration, Plans, approvals, interruption points and assertions as the direct-local row, run from identical clean snapshots. Transport loss before dispatch and after remote completion around every Database operation (candidate creation, synchronization, write fence, authority switch, backup, restore verification, retention marking) recovered by observing the remote Host first, with no data-bearing step replayed. Logical Database, Store Generation identities, workload records, backup and restore evidence, retained material and owned-resource inventory were identical to direct-local in all three modes. VM restored clean afterward. See the [remote evidence](evidence/2026-10-10-postgresql-remote-ssh-parity.md) and [parity record](evidence/2026-10-10-postgresql-remote-ssh-parity.json). |
| `scripts/test-direct-local-postgresql-boundaries.sh` via `scripts/run-direct-local-postgresql-boundaries.sh` (`candidate-failure` and `forward-only` scenarios) | Same VM, direct-local | Same `17.6` to `17.7` transition and versions as the rows above | A candidate stopped after preparation fails synchronization with outcome `failed`; the active generation, its data, the workload binding and the absence of authority-switch evidence are unchanged, and a direct authority switch is refused. After a completed cutover, a write accepted by the new generation is absent from the pre-cutover backup and retained generation, so rollback stays classified `forward-only`. See the [boundary evidence](evidence/2026-10-10-postgresql-candidate-failure-and-forward-only.md). |

`17.6` remains the default when a Database implementation declares no `version`.
Only `17.6` and `17.7` are registered in `hostdatabase.Images`; other patch
versions, minor-version differences beyond the same major, and image
manifests remain unqualified.

## Managed PostgreSQL Host transition contract

| Test seam | Qualified input | Operation contract | Result |
| --- | --- | --- | --- |
| Go planner, restricted-executor, and local result-verifier tests for issue #73 | Existing active managed PostgreSQL `17.6` generation with matching image manifest, ready SQL connectivity, and authoritative role; candidate target is the exact qualified PostgreSQL `17.6` generation above | `prepareDatabaseCandidate`, `synchronizeDatabaseCandidate`, `fenceDatabaseWrites`, `switchDatabaseAuthority`, `verifyDatabaseActive`, and `retainDatabasePrevious` | Plan preview exposes lossless forward cutover after the explicit `active-database-read-only-with-session-termination` fence with `30s` maximum bound, keeps rollback classified as `forward-only`, uses separate candidate service/container/listener before authority switch, rejects incompatible active generations, rejects unsafe observed candidates, requires phase-specific signed Database results, verifies declared deterministic workload records directly from PostgreSQL after authority switch, and retains the previous generation with exact identity and policy metadata. |

This row qualifies the contract shape and executor guardrails only. It does not
replace live disposable-Host transition evidence, and it does not qualify
production data migration, major-version upgrades, logical-replication DDL or
sequence restrictions, reverse synchronization, automatic zero-loss rollback,
backup/restore, host-loss recovery, RDS, or external PostgreSQL.

## Managed PostgreSQL Host backup and isolated restore contract

| Test seam | Qualified input | Operation contract | Result |
| --- | --- | --- | --- |
| Go config, planner, restricted-executor, and local result-verifier tests for issue #74 | Managed authoritative PostgreSQL `17.6` with a declared local `file://` backup destination outside the active Store Generation and `hostLoss: not-declared` | `backupDatabase` and `verifyDatabaseRestore` | Plans create non-secret backup evidence after the authoritative generation/transition, then verify restore into an isolated restore candidate. Same-host `file://` storage is rejected for declared host-loss recovery; `ssh://` execution is the separate #92 row below. |
| Go config, planner, restricted-executor (fake `ssh` shim), and result-verifier tests for issue #92; live proof for issue #94 ([evidence](evidence/2026-10-13-postgresql-offhost-ssh.md)) | Managed authoritative PostgreSQL `17.6` with an `ssh://` backup destination, `sshUser`, pinned `hostKey` and a key Secret Reference, `hostLoss: off-host-backup-required` | `backupDatabase` and `verifyDatabaseRestore` | **Qualified for exactly the tested combination:** Ubuntu 26.04 `x86_64` Host Target over direct-local and strict SSH, destination a second Ubuntu Incus VM (OpenSSH, ed25519 host key pin, `pgbackup` account) on the same Incus host ([ADR 0078](adr/0078-execute-off-host-ssh-database-backup.md)). Dump is uploaded over pinned-host-key SSH with remote digest verification; evidence records artifact digest and destination host identity; restore verification fetches and digest-checks before restoring. Unreachable, host-key mismatch, auth failure and partial upload are `failed`; response loss after upload is `uncertain` and recovery observes before acting. Live: success, destination unreachable and host-key mismatch pass on both transports, with the destination's own `sha256sum` matching the evidence digest. Not qualified: independent hardware or network failure domains, non-Linux or non-OpenSSH destinations, and live partial-upload or response-loss injection (fake-`ssh` tests only). |

Issue #125's restricted-executor tests stream 32 MiB of generated dump data to
each of `file://` and fake-SSH destinations within an 8 MiB Go allocation budget,
including local digest read-back. Both paths use partial files and digest checks
before rename, then read back evidence; source failure cannot commit a partial
dump, and retry reuses committed data. Early SSH failure cancels a blocked export;
digest mismatch, partial upload and response-loss recovery are covered by fake
SSH. This removes executor whole-dump buffering, but does not extend the live
qualification above to production-sized Databases or establish a recovery-time
bound.

This row qualifies backup/restore contract shape, same-host logical backup
evidence, and isolated restore-verification guardrails. It does not qualify
Host-loss recovery, off-host transfer execution, encrypted backing media,
retention expiry cleanup, RDS snapshots/PITR, or production recovery-time
behavior.

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

## Complete asynchronous direct-local and remote SSH parity

| Provision build | Controller and transport | Host OS and runtime | Async topology | Result |
| --- | --- | --- | --- | --- |
| source evidence `506bd3e03f2f91f207b1cbddc45c8173fbb7b697-dirty-3bde0f05c37df4cd1dc3367f1c3d4f64fff7c2560b00b60c3942bd1782ffd7ee`; macOS `arm64` controller binary `sha256:5f6b4a090d49bf810ca0ecdc18f2700dcecbf510a10178894e90e1a9c1828bc2`; Linux `x86_64` direct-local binary `sha256:6fde40576b06e61c2131afc5e137d1254f976063287a0e4f986899a1631be5c7`; Linux executor and schedule applet `sha256:0b62d993842142e894acc52c5b7810cb4f5cb91c958ac51d255c2379d77a9607` | direct-local inside the VM and strict OpenSSH from Darwin `24.6.0` `arm64`; pinned host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs`; password authentication disabled | Ubuntu Server 26.04 VM, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6`; Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6` at manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` | one managed Queue, one systemd Worker, one systemd Task, one stable Schedule, schedule ledger `provision.dev/schedule-ledger/v1alpha1` | The direct-local and remote SSH matrices passed from clean VM restores with the same configuration, planner, approval, operation sequence, application artifacts, recovery harnesses, and final accounting. Both transports matched Host OS/version/arch, systemd, executor digest, RabbitMQ manifest, schedule applet digest, message and occurrence behavior, Task Invocation identity accounting, active/retained Generation accounting, owned-resource inventory, and artifact/runtime versions. Remote SSH injected loss around Queue preparation, Worker fence/drain/activation/verification/retention, Schedule handoff/verification, and Task delivery, then proved the stable Schedule resumed after Host reboot. See the [parity evidence](evidence/2026-09-27-remote-ssh-async-parity.md). |

This row qualifies remote-management parity for the complete asynchronous
single-Host tracer. It does not qualify Queue generation migration,
exactly-once delivery or processing, autoscaling, other Host OS or dependency
versions, AWS, ECS, Lambda, realtime servers, databases, caches, store
transitions, Host data-loss recovery, or RabbitMQ data-loss recovery.

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
- The complete asynchronous tracer has equivalent direct-local and remote-SSH
  management behavior for the qualified single-Host path: the same Plans and
  operation handlers survive controller loss around Queue preparation, Worker
  handoff, Schedule handoff, and Task delivery, and the remote Host continues
  its stable Schedule after reboot without a resident Provision controller.
- The managed PostgreSQL packaging proof starts an exact digest-pinned
  rootless Quadlet generation, proves SQL connectivity and loopback-only
  exposure, records a non-secret credential reference, stores data in the
  generation-owned path, and proves the same database data is present after a
  full Host reboot.

## Limits

- The HTTP rows cover a single native `x86_64` HTTP component on one systemd-managed host, exercised both directly on the host and remotely from an `arm64` macOS controller over SSH. The RabbitMQ rows qualify only the exact rootless Podman/Quadlet packaging and Queue semantics stated above. The PostgreSQL packaging row qualifies only the exact Host-local packaging and restart proof stated above. The PostgreSQL transition-contract row qualifies only Plan/executor guardrails for a same-version forward-only transition, not production durability or live fault-injection evidence. The scheduled-message row qualifies only its exact initial systemd Worker, Task, and Schedule path. The Worker rows qualify pre-handoff rejection, completed replacement, message-level activation, supported rollback, and controller-interruption recovery only for the stated single-host examples. The asynchronous parity row qualifies only direct-local and strict-SSH management parity for the exact single-Host Queue, Worker, Task, and Schedule tracer. No row qualifies EC2, ECS, Lambda, key-value stores, realtime services, Queue replacement, Queue migration, Host/data-loss recovery, RabbitMQ data-loss recovery, PostgreSQL host-loss recovery, or multi-host asynchronous availability.
- The Endpoint guarantee covers ordinary HTTP requests. It does not promise WebSocket or other long-lived stream draining.
- Caddy configuration reload preserves the prior route on a rejected load. An external Caddy outage can still make the Endpoint unavailable until Caddy is restored.
- Remote mode adds a management-transport dependency, not a workload-availability dependency. If the controller cannot authenticate the host or cannot obtain enough fresh evidence after a lost connection, the operation remains interrupted or uncertain until an operator restores access and resumes it; stable traffic continues according to the last host state.
- SSH host-key rotation is deliberately not automatic. A reset or legitimate key change fails closed until the operator verifies and pins the replacement out of band.
- Candidate verification, Endpoint switching, stable verification, bounded rollback, bounded ordinary-HTTP drain completion, and rollback-window recording are implemented. The latest direct-local and remote-SSH rows support-qualify that complete sequence through the same engine seams. Historical rows predate parts of that sequence. Expiry-time cleanup is not implemented.
- A successfully drained previous Generation is stopped while its exact unit definition, immutable Generation directory and manifest, and digest-addressed Artifact remain installed and restartable. Other failed or older Generations may remain running because cleanup is not yet enabled.
- Host reset and bootstrap are separate operator-controlled procedures. The matrix does not claim unattended OS provisioning, upgrades, or in-place migration from Gimme.
- Versions not listed above require their own capability observation and acceptance run; they are not implied by this row.

The remote PostgreSQL row qualifies only the exact combination recorded in its parity record. It excludes RDS, Valkey, object stores, multi-node PostgreSQL availability, production disaster recovery, external PostgreSQL adoption, automatic cleanup, and untested host, runtime, or SSH/controller versions.
