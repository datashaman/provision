# Base host rollback-window evidence — 2026-09-25

Issue #30 was exercised on the disposable `base` host after a fresh Ubuntu Server installation and Provision bootstrap. The acceptance binary was built from reviewed commit `2090ea6a2887c5011fc3801915f19767bacab82a` for Linux `x86_64`. The checked-in direct-local harness completed all seven scenarios in one run and printed `direct-local failure matrix passed`.

## Observed platform

- Ubuntu Server `26.04`
- Linux `7.0.0-34-generic`
- systemd `259 (259.5-0ubuntu3.4)`
- Caddy `2.6.2`
- Provision binary: `sha256:1542523d7461c68893de71863cfec4b43eaa7a55a098646c3ddbdaac3f0acd92`
- restricted host executor: `sha256:a3dbdddb1b8a8c936a600219df66e983f988baa8ae834744c817645c7297964d`
- authority key ID: `sha256:9816ed35b52d1e829fec00e3b998c6e679d980e4bed4f40f308ce4e2475038d4`
- SSH host fingerprint: `SHA256:7EKOYkQPEQt2Xee3P3/2mqT8Y2o/ccoD//wNIWDyuBg`

Bootstrap inspection was `ready: true` with no findings before the run. The initial host had no Provision Deployment state, stable Endpoint, application units, Generation directories, `/srv/gimme`, or `gimme-*` units. The installed executor advertised `retainPrevious` in addition to the preceding seven typed operations.

## Matrix result

| Scenario | Observation |
| --- | --- |
| Healthy rollout | v1 passed candidate and stable checks and became active. |
| Pre-switch verification failure | The deliberately invalid candidate failed before traffic switching; the stable Endpoint did not change. |
| Endpoint switch failure | Caddy was deliberately stopped. Provision recorded uncertainty, Caddy was restored, and explicit resume completed the switch from fresh host evidence. The exact drained previous Generation was then recorded through its rollback window. |
| Bounded ordinary-HTTP drain | A one-second request started against v2 before the v3 switch and completed on v2. Provision honored the declared two-second handoff bound and stopped only the exact v2 unit. |
| Lost drain response | The CLI was deliberately killed after the host completed `op-07`. Resume observed the durable drain marker and recorded `drained` without replaying the host mutation. |
| Rollback-window recording | `op-08` proved the exact stopped v2 unit, immutable Generation directory and manifest, digest-addressed Artifact, active v3 unit, and stable v3 route before recording v2 as restartable. It performed no cleanup. |
| Lost rollback-window response | The CLI was deliberately killed after the host durably completed `op-08`. After lease expiry, resume observed that exact marker and returned `retained` without redispatch. |
| Post-switch failure | The `fail-stable` candidate failed through the stable Endpoint and automatically restored the directly verified previous Generation. Its dependent drain and rollback-window operations did not proceed. |
| Process interruption | The CLI was deliberately killed after a traffic switch. Resume proved the already-completed switch without replaying it. |
| Stale executor | A separately authorized older lineage was rejected after a newer fencing token advanced host authority. Stable traffic did not move. |

The shell's three `Killed` diagnostics were the expected lost drain response, lost rollback-window response, and post-switch process interruption. They were not failed assertions.

## Rollback-window proof

The deterministic v3 Plan bound `op-08` to:

- active Generation `provision-example-http-v3-4e775436b605` and its stable `provision-lab-web` route;
- previous Generation `provision-example-http-v2-f5d67ce429e0`;
- previous unit `provision-lab-web-f5d67ce429e0.service`;
- previous Artifact `sha256:f5d67ce429e0eedfd6d2a73be9b775b9a05025853d13c63a004164ce89bc9995`;
- `policy: rollback-window` and `rollbackWindow: 30m0s`; and
- successful completion of the exact `op-07` dependency.

The Plan contained no wall-clock deadline. Execution recorded:

- switch time `2026-09-25T12:18:09.027511813Z`;
- drain completion `2026-09-25T12:18:11.632589666Z`;
- rollback-window record time `2026-09-25T12:18:18.361681712Z`; and
- deadline `2026-09-25T12:48:09.027511813Z`.

An independent nanosecond-precision comparison confirmed that the deadline is exactly 1,800 seconds after the trusted switch time. The successful observation bound operation digest `sha256:f0dc854245a159900a25b2ba129d08cd1baa455302b97fde54f6778422412985` to drain digest `sha256:e0a2c74634e862ec7d6389e00e7c5fa557951a180bdbf018c2bf75b66b401bbb` and reported:

- `status: retained`;
- `stableRouteVerified: true`;
- previous unit inactive but retained;
- previous Generation directory and manifest retained;
- previous Artifact retained;
- `restartable: true`; and
- `cleanupPerformed: false`.

The journal kept the interrupted first `op-08` intent. A second intent named it through `resumeOfAttemptId` under a higher fencing token, then committed the successful observation. The fault wrapper was not invoked on resume, proving observation-only reconciliation rather than replay.

At the rollback-window checkpoint, the exact expected v1, v2, and v3 unit, Generation-directory, and Artifact inventories matched. The harness verified cached Artifact digests, compared retained-file checksums, checked stable v3 health, and confirmed that Gimme resources remained absent.

## Qualification boundary

This run support-qualifies rollback-window recording for the direct-local native systemd/Caddy HTTP implementation on the exact platform above. It proves that the exact previous Generation remains restartable through a switch-derived deadline and that recording performs no cleanup.

It does not qualify expiry-time garbage collection, quarantine or deletion, remote-SSH rollback-window behavior, WebSocket draining, OCI/Podman, another OS/dependency combination, or non-HTTP component roles.
