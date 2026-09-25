# Test-backed support matrix

This matrix records combinations exercised end to end. It is evidence of the stated guarantees for the exact combination, not a claim that every nearby OS or dependency version is supported.

## Direct-local HTTP host Deployment

| Provision build | Host OS | Architecture | systemd | Caddy | Result |
| --- | --- | --- | --- | --- | --- |
| commit `00f8b0c`, binary `sha256:82eb365b4033980eaa780e08a8a57eb746bcc69bcbbaed361e06af4e4c8b1d5a` | Ubuntu Server 26.04, kernel `7.0.0-34-generic` | `x86_64` | `259 (259.5-0ubuntu3.4)` | `2.6.2` | Healthy rollout and all five failure classes observed on `base`; see the [2026-09-25 evidence](evidence/2026-09-25-base-host-direct-local-matrix.md). |

The matching host executor digest was `sha256:1fdf17dbecb277e8e1e6601f6eb3807a04505422843dbe4654b1e0a1f1e6580b`.

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
