# Test-backed support matrix

This matrix records combinations exercised end to end. It is evidence of the stated guarantees for the exact combination, not a claim that every nearby OS or dependency version is supported.

## Direct-local HTTP host Deployment

| Provision build | Host OS | Architecture | systemd | Caddy | Result |
| --- | --- | --- | --- | --- | --- |
| commit `b4ee92789c242056b5fbe9a51ab42b8c5d02bc36`, binary `sha256:d4baa0bf57f3620ae1e473a748391eb55700679331267762087ad1e3f2c78229` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout and all five failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-direct-local-matrix.md). |
| commit `a2f7788b057c7e5afc686ec1a10e21d1528e01e2`, binary `sha256:f7ac2386c5dba47463cba4f46d9f1b7f83640656dc1aa223fe92625a5a6aea4a` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout, bounded ordinary-HTTP drain, lost-response recovery, and all failure classes passed in one repeatable seven-scenario run on `base`; see the [bounded-drain evidence](evidence/2026-09-25-base-host-bounded-http-drain.md). |

The matching host executor digests were `sha256:c532f3b78d291c400761b187c1fe8c14e84f1427b9d59498d71d1f73e6f0b21e` for the historical row and `sha256:af16725d2baa1b65cb5e38d1e24d8acc3f401d2992dde317e7431432ec8520de` for the bounded-drain row. The harness emitted each row's values with `result: passed` in its structured support observation.

## Remote SSH HTTP host Deployment

| Provision build | Controller | Remote Host | SSH identity | Result |
| --- | --- | --- | --- | --- |
| commit `6598d23f0182d858713da1effb1462abee662b5c`, binary `sha256:caff13cd97adcfde3ea9b6573a59f8e601480cebd015f47c4520b7c1b4dfa349` | macOS, Darwin `24.6.0`, `arm64` | Ubuntu Server 26.04, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Caddy `2.6.2`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` | strict `known_hosts`; ED25519 `SHA256:E6M0uy67VlvnLBWjZhP+Ltk6v7wDS1yYo3eyqBOut6Y` | Healthy rollout plus all six remote failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-remote-ssh-matrix.md). |

The matching remote executor digest was `sha256:e9c910de10f89045c98d426454c60a3e7e467e3ed7c12c6994bda91bc93f6c38`. The authority private key, approvals, and State Backend remained on the controller. The host received only observations and signed typed operations through a restricted one-shot executor.

## Guarantees exercised

- An unverified candidate cannot receive stable traffic.
- A failed Caddy switch does not silently become success; explicit recovery re-observes the host before continuing.
- A stable health failure restores traffic only after proving the exact retained previous Generation healthy both directly and through the stable Endpoint.
- A process interrupted after the traffic switch resumes from fresh host evidence without replaying the switch.
- After stable verification, the direct-local native HTTP implementation honors the complete declared Caddy handoff bound before stopping only the exact previous Generation; its unit definition and immutable release remain available.
- A lost successful drain response remains interrupted until resume proves durable host completion, then records `drained` without replaying the stop.
- A stale executor authorization cannot mutate the host or move the stable Endpoint.
- The durable journal distinguishes successful, failed, interrupted, resumed, and uncertain attempts.
- Only the expected Provision units and immutable release directories appear; Gimme-owned resources remain absent.
- Over SSH, losing a completed operation's response does not become success or cause blind replay. A new Provision process and a new one-shot remote executor reconcile the journal against fresh host evidence.
- Over SSH, a changed or unknown host key fails before the executor is invoked.
- An in-flight SSH disconnect and killed Artifact-stage executor produce an uncertain journal outcome. Resume removes only temporary files proven to belong to an earlier signed attempt from the same Environment before safely restaging.

## Limits

- These rows cover a single native `x86_64` HTTP component on one systemd-managed host, exercised both directly on the host and remotely from an `arm64` macOS controller over SSH. They do not qualify OCI/Podman, EC2, ECS, Lambda, workers, schedules, databases, key-value stores, or realtime services.
- The Endpoint guarantee covers ordinary HTTP requests. It does not promise WebSocket or other long-lived stream draining.
- Caddy configuration reload preserves the prior route on a rejected load. An external Caddy outage can still make the Endpoint unavailable until Caddy is restored.
- Remote mode adds a management-transport dependency, not a workload-availability dependency. If the controller cannot authenticate the host or cannot obtain enough fresh evidence after a lost connection, the operation remains interrupted or uncertain until an operator restores access and resumes it; stable traffic continues according to the last host state.
- SSH host-key rotation is deliberately not automatic. A reset or legitimate key change fails closed until the operator verifies and pins the replacement out of band.
- Candidate verification, Endpoint switching, stable verification, bounded rollback, and bounded ordinary-HTTP drain completion are implemented. The direct-local combination in the bounded-drain row support-qualifies that drain behavior; the historical direct-local and remote-SSH rows predate it and do not. Rollback-window cleanup is not implemented.
- A successfully drained previous Generation is stopped while its unit definition and immutable release remain installed. Other failed or older Generations may remain running because rollback-window retention and cleanup are not yet enabled.
- Host reset and bootstrap are separate operator-controlled procedures. The matrix does not claim unattended OS provisioning, upgrades, or in-place migration from Gimme.
- Versions not listed above require their own capability observation and acceptance run; they are not implied by this row.
