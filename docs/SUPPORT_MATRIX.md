# Test-backed support matrix

This matrix records combinations exercised end to end. It is evidence of the stated guarantees for the exact combination, not a claim that every nearby OS or dependency version is supported.

## Direct-local HTTP host Deployment

| Provision build | Host OS | Architecture | systemd | Caddy | Result |
| --- | --- | --- | --- | --- | --- |
| commit `b4ee92789c242056b5fbe9a51ab42b8c5d02bc36`, binary `sha256:d4baa0bf57f3620ae1e473a748391eb55700679331267762087ad1e3f2c78229` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout and all five failure classes passed in one repeatable run on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-direct-local-matrix.md). |

The matching host executor digest was `sha256:c532f3b78d291c400761b187c1fe8c14e84f1427b9d59498d71d1f73e6f0b21e`. The harness emitted these values with `result: passed` in its structured support observation.

## Guarantees exercised

- An unverified candidate cannot receive stable traffic.
- A failed Caddy switch does not silently become success; explicit recovery re-observes the host before continuing.
- A stable health failure restores traffic only after proving the exact retained previous Generation healthy both directly and through the stable Endpoint.
- A process interrupted after the traffic switch resumes from fresh host evidence without replaying the switch.
- A stale executor authorization cannot mutate the host or move the stable Endpoint.
- The durable journal distinguishes successful, failed, interrupted, resumed, and uncertain attempts.
- Only the expected Provision units and immutable release directories appear; Gimme-owned resources remain absent.

## Limits

- This row covers a single native `x86_64` HTTP component on a direct-local, systemd-managed host. It does not qualify remote SSH transport, OCI/Podman, EC2, ECS, Lambda, workers, schedules, databases, key-value stores, or realtime services.
- The Endpoint guarantee covers ordinary HTTP requests. It does not promise WebSocket or other long-lived stream draining.
- Caddy configuration reload preserves the prior route on a rejected load. An external Caddy outage can still make the Endpoint unavailable until Caddy is restored.
- Candidate verification, Endpoint switching, stable verification, and bounded rollback are implemented. Role-specific drain completion and rollback-window cleanup are not.
- Previous and failed Generations remain installed and their systemd services may remain running because retention cleanup is not yet enabled.
- Host reset and bootstrap are separate operator-controlled procedures. The matrix does not claim unattended OS provisioning, upgrades, or in-place migration from Gimme.
- Versions not listed above require their own capability observation and acceptance run; they are not implied by this row.
