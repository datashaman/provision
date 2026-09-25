# Test-backed support matrix

This matrix records combinations exercised end to end. It is evidence of the stated guarantees for the exact combination, not a claim that every nearby OS or dependency version is supported.

## Direct-local HTTP host Deployment

| Provision build | Host OS | Architecture | systemd | Caddy | Result |
| --- | --- | --- | --- | --- | --- |
| commit `b4ee92789c242056b5fbe9a51ab42b8c5d02bc36`, binary `sha256:d4baa0bf57f3620ae1e473a748391eb55700679331267762087ad1e3f2c78229` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout and all five failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-direct-local-matrix.md). |

The matching host executor digest was `sha256:c532f3b78d291c400761b187c1fe8c14e84f1427b9d59498d71d1f73e6f0b21e`. The harness emitted these values with `result: passed` in its structured support observation.

## Remote SSH HTTP host Deployment

| Provision build | Controller | Remote Host | SSH identity | Result |
| --- | --- | --- | --- | --- |
| commit `051460d7040ad6bae5cc0ac0c33ed2ef45daccd6`, binary `sha256:f467d84db5031f990a99770a8f20ed88afa08c35eeccfc78be8d1c02677eb862` | macOS, Darwin `24.6.0`, `arm64` | Ubuntu Server 26.04, kernel `7.0.0-34-generic`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; Caddy `2.6.2`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` | strict `known_hosts`; ED25519 `SHA256:Wncb+SL2zJfiTjCVKeOVuYqJIZ3zC9yhIfPiR2B9qIo` | Healthy rollout and all five remote failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-remote-ssh-matrix.md). |

The matching remote executor digest was `sha256:d3e958df7d6814df3510ad875f3c5e53e7ad82936e4aaf9f01ec263916110f77`. The authority private key, approvals, and State Backend remained on the controller. The host received only observations and signed typed operations through a restricted one-shot executor.

## Guarantees exercised

- An unverified candidate cannot receive stable traffic.
- A failed Caddy switch does not silently become success; explicit recovery re-observes the host before continuing.
- A stable health failure restores traffic only after proving the exact retained previous Generation healthy both directly and through the stable Endpoint.
- A process interrupted after the traffic switch resumes from fresh host evidence without replaying the switch.
- A stale executor authorization cannot mutate the host or move the stable Endpoint.
- The durable journal distinguishes successful, failed, interrupted, resumed, and uncertain attempts.
- Only the expected Provision units and immutable release directories appear; Gimme-owned resources remain absent.
- Over SSH, losing a completed operation's response does not become success or cause blind replay. A new Provision process and a new one-shot remote executor reconcile the journal against fresh host evidence.
- Over SSH, a changed or unknown host key fails before the executor is invoked.

## Limits

- These rows cover a single native `x86_64` HTTP component on one systemd-managed host, exercised both directly on the host and remotely from an `arm64` macOS controller over SSH. They do not qualify OCI/Podman, EC2, ECS, Lambda, workers, schedules, databases, key-value stores, or realtime services.
- The Endpoint guarantee covers ordinary HTTP requests. It does not promise WebSocket or other long-lived stream draining.
- Caddy configuration reload preserves the prior route on a rejected load. An external Caddy outage can still make the Endpoint unavailable until Caddy is restored.
- Remote mode adds a management-transport dependency, not a workload-availability dependency. If the controller cannot authenticate the host or cannot obtain enough fresh evidence after a lost connection, the operation remains interrupted or uncertain until an operator restores access and resumes it; stable traffic continues according to the last host state.
- SSH host-key rotation is deliberately not automatic. A reset or legitimate key change fails closed until the operator verifies and pins the replacement out of band.
- Candidate verification, Endpoint switching, stable verification, and bounded rollback are implemented. Role-specific drain completion and rollback-window cleanup are not.
- Previous and failed Generations remain installed and their systemd services may remain running because retention cleanup is not yet enabled.
- Host reset and bootstrap are separate operator-controlled procedures. The matrix does not claim unattended OS provisioning, upgrades, or in-place migration from Gimme.
- Versions not listed above require their own capability observation and acceptance run; they are not implied by this row.
