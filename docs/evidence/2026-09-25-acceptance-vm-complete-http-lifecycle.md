# Complete HTTP host lifecycle evidence — 2026-09-25

Issue #31 was exercised against the disposable `provision-acceptance` Ubuntu VM from reviewed source commit `8ddd3bdd0c2920cf93988125392620e22979b21b`. The VM was restored from the same stopped `clean` snapshot and bootstrapped independently before each run. The direct-local and remote-SSH harnesses each completed all seven scenarios and printed their respective success marker.

## Tested combination

| Property | Direct-local | Remote SSH |
| --- | --- | --- |
| Provision source | `8ddd3bdd0c2920cf93988125392620e22979b21b` | same |
| Provision binary | Linux `x86_64`, `sha256:1542523d7461c68893de71863cfec4b43eaa7a55a098646c3ddbdaac3f0acd92` | macOS `arm64`, `sha256:5d1f3a67368fad6eab0ea22ee9324fe8c334c22c33f0cfd584dffd4d4e0efd01` |
| Restricted executor | `sha256:a3dbdddb1b8a8c936a600219df66e983f988baa8ae834744c817645c7297964d` | same |
| Authority key ID | `sha256:a24d89100b4b9ba9941796135eac7ea81d735dcd84d18f91041cdab88b753bb0` | same public verifier; private key remained on the controller |
| Host | Ubuntu Server `26.04`, Linux `7.0.0-34-generic`, `x86_64` | same clean VM image |
| Services | systemd `259 (259.5-0ubuntu3.4)`, Caddy `2.6.2` | same, plus OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Controller | the Linux Host Target | macOS, Darwin `24.6.0`, `arm64` |
| SSH policy | not applicable | strict `known_hosts`, ED25519 `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |

Bootstrap inspection was `ready: true` with no findings before each matrix. It advertised the same eight typed mutations through the installed executor: Artifact staging, Generation installation, candidate start and verification, Endpoint switch, active verification, previous-Generation drain, and previous-Generation retention.

## Equivalent lifecycle proof

Both transports used `plan preview`, exact-Plan approval, and the same deployment execute/resume/status engine. Each proved:

- healthy first deployment;
- candidate verification failure before traffic movement;
- uncertain Caddy switch failure followed by explicit observation-based recovery;
- candidate preparation, switch, stable verification, bounded drain, and rollback-window retention;
- automatic recovery from failed post-switch verification;
- process interruption after traffic movement followed by observation before resume; and
- rejection of a stale executor fencing token without moving stable traffic.

For the v2-to-v3 handoff, a one-second ordinary HTTP request began before the switch and completed on v2 while new stable traffic moved to v3. Only the exact v2 unit stopped after the declared two-second bound. The unit definition, immutable Generation directory and manifest, and digest-addressed Artifact remained installed and restartable.

The direct-local run separately lost the successful `drainPrevious` and `retainPrevious` responses. The remote run did the same by withholding completed executor results across SSH. In both runs the journal retained intent, a later attempt carried resume provenance under a higher fencing token, and fresh host observation returned `drained` and `retained` without redispatching either completed mutation.

Both retention results bound the same deterministic operation digest `sha256:7ec8723571911af468e528464e7ad3db71f5c5f44acd932c8ca8d3f145882d7d` to drain digest `sha256:e0a2c74634e862ec7d6389e00e7c5fa557951a180bdbf018c2bf75b66b401bbb`. Independent integer-nanosecond checks proved each `retainUntil` was exactly 1,800 seconds after its trusted `switchedAt`:

| Transport | Switched | Retain until |
| --- | --- | --- |
| Direct-local | `2026-09-25T13:07:57.735710922Z` | `2026-09-25T13:37:57.735710922Z` |
| Remote SSH | `2026-09-25T13:11:17.540073897Z` | `2026-09-25T13:41:17.540073897Z` |

Each result reported `stableRouteVerified: true`, the previous unit inactive but retained, all restart material retained, `restartable: true`, and `cleanupPerformed: false`.

## Inventory and isolation

After every scenario, each harness compared exact expected and actual Artifact, systemd-unit, and Generation-directory sets, checked every cached Artifact digest, preserved checksums for previously retained material, verified Caddy configuration outside Provision's application-scoped server was unchanged, and required Caddy to remain active.

The final inventories matched across transports: four immutable Artifacts, four application units, and four Generation directories for v1, v2, v3, and the deliberately failing post-switch candidate. Stable traffic ended on v1 with v3 recorded as the previous Generation after the interruption scenario. No abandoned temporary Artifact material was present. `/srv/gimme`, `gimme-*` units, and other Gimme-owned paths, routing, stores, and state remained absent throughout.

## Transport boundary

Remote mode additionally proved strict host-key verification, failure after an in-flight SSH disconnect and executor termination, recovery through a new SSH connection and one-shot executor, and loss of completed operation responses without converting ambiguity into success. This adds a management-transport dependency but no workload-availability dependency. The externally visible workload and retained host inventory were otherwise equivalent to direct-local mode.

## Qualification boundary and closure decision

This evidence support-qualifies the complete native systemd/Caddy HTTP Host Deployment lifecycle for the exact versions above through both direct-local and agentless SSH execution. It is sufficient to close issue #31 and the scoped parent tracer in issue #1: all in-scope operations, failure boundaries, parity requirements, framework-neutral fixture behavior, and Gimme isolation have real-host evidence.

It does not qualify WebSocket or other long-lived-stream draining, expiry-time cleanup or garbage collection, OCI/Podman, other component roles, other host versions, EC2 provisioning, ECS, Lambda, or AWS managed services. VM reset and foundational host bootstrap remain explicit external operator-controlled procedures rather than Deployment operations.
