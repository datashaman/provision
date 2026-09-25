# Base host bounded HTTP drain evidence — 2026-09-25

Issue #29 was exercised on the disposable `base` host after a fresh Ubuntu Server installation and Provision bootstrap. The acceptance binary was built from commit `a2f7788b057c7e5afc686ec1a10e21d1528e01e2` for Linux `x86_64`. The checked-in direct-local harness completed all seven scenarios in one run and printed `direct-local failure matrix passed`.

## Observed platform

- Ubuntu Server `26.04`
- Linux `7.0.0-34-generic`
- systemd `259 (259.5-0ubuntu3.4)`
- Caddy `2.6.2`
- Provision binary: `sha256:f7ac2386c5dba47463cba4f46d9f1b7f83640656dc1aa223fe92625a5a6aea4a`
- restricted host executor: `sha256:af16725d2baa1b65cb5e38d1e24d8acc3f401d2992dde317e7431432ec8520de`
- authority key ID: `sha256:82808608c94f1facf35e25208fd13093da649825fa41b285a4d825a2dd602a3e`
- SSH host fingerprint: `SHA256:XInh/B+PDaB9VC9yW3RevAQCgXEXsGFvqgIyfYJmYKA`

Bootstrap inspection was `ready: true` before the run. The initial host had no Provision Deployment state, stable Endpoint, Provision application units, Provision releases, `/srv/gimme`, or `gimme-*` units. The installed executor advertised the typed `drainPrevious` operation.

## Matrix result

| Scenario | Observation |
| --- | --- |
| Healthy rollout | v1 passed candidate and stable checks and became active. |
| Pre-switch verification failure | The deliberately invalid candidate failed before traffic switching; the stable Endpoint did not change. |
| Endpoint switch failure | Caddy was deliberately stopped. Provision recorded uncertainty, Caddy was restored, and explicit resume completed the switch from fresh host evidence. |
| Bounded ordinary-HTTP drain | A one-second request started against v2 before the v3 switch and completed with `{"revision":"provision-example-http-v2"}`. After v3 passed stable verification, Provision honored the declared two-second handoff bound and stopped only the exact v2 unit. |
| Lost drain response | The CLI was deliberately killed after the host completed `op-07` but before recording the result. The journal retained intent; after lease expiry, resume observed the durable completion marker and returned `status: drained` without invoking the host mutation again. |
| Post-switch failure | The `fail-stable` candidate failed through the stable Endpoint and automatically restored the directly verified retained Generation. |
| Process interruption | The CLI was deliberately killed after a traffic switch. Resume proved the already-completed switch without replaying it. |
| Stale executor | A separately authorized older lineage was rejected at its first required host mutation after a newer fencing token had advanced host authority. Stable traffic did not move. |

The shell's two `Killed` diagnostics were the expected drain-response-loss and process-interruption injections, not failed assertions.

## Drain proof

The exact v3 Plan bound `op-07` to:

- active Generation `provision-example-http-v3-4e775436b605`;
- previous Generation `provision-example-http-v2-f5d67ce429e0`;
- previous unit `provision-lab-web-f5d67ce429e0.service`;
- handoff policy `caddy-graceful-config-reload`;
- mode `bounded-http`; and
- maximum duration `2s`.

The successful resumed result recorded operation digest `sha256:e0a2c74634e862ec7d6389e00e7c5fa557951a180bdbf018c2bf75b66b401bbb`. Stable verification completed at `2026-09-25T09:26:50.980466561Z`; the drain deadline was exactly two seconds later at `2026-09-25T09:26:52.980466561Z`. The result reported `boundElapsed: true` and `stableRouteVerified: true`.

Fresh host observation then proved:

- v3 remained active and routed at the stable Endpoint;
- the exact v2 unit was inactive;
- the v2 unit definition still matched;
- the immutable v2 release remained installed; and
- bootstrap inspection remained `ready: true` with no findings.

At the drain checkpoint, the only application units, releases, and cached Artifacts were the expected v1, v2, and v3 sets. The harness compared those exact inventories, verified retained-file checksums, checked stable v3 health, and confirmed that Gimme resources remained absent.

## Qualification boundary

This run support-qualifies the direct-local native systemd/Caddy implementation's bounded ordinary-HTTP drain on the exact platform listed above. It demonstrates a request whose response spans the route handoff, a full declared post-verification bound, an exact previous-unit stop, retention, and observation-only recovery from a lost completion response.

It does not claim WebSocket or other long-lived-stream draining, remote-SSH drain behavior, retention cleanup, OCI/Podman, another OS/dependency combination, or non-HTTP component roles.
