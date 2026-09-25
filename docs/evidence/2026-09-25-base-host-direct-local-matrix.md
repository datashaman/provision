# Base host direct-local failure matrix evidence — 2026-09-25

Issue #12 was exercised on the disposable `base` host after a fresh Ubuntu Server installation and Provision bootstrap. The acceptance binary was built from commit `00f8b0c` for Linux `x86_64`.

## Observed platform

- Ubuntu Server `26.04`
- Linux `7.0.0-34-generic`
- systemd `259 (259.5-0ubuntu3.4)`
- Caddy `2.6.2`
- Provision binary: `sha256:82eb365b4033980eaa780e08a8a57eb746bcc69bcbbaed361e06af4e4c8b1d5a`
- restricted host executor: `sha256:1fdf17dbecb277e8e1e6601f6eb3807a04505422843dbe4654b1e0a1f1e6580b`

Bootstrap inspection was ready before the run. The initial host had no Provision Deployment state, stable Endpoint, Provision application units, Provision releases, `/srv/gimme`, or `gimme-*` units.

## Results

| Scenario | Observation |
| --- | --- |
| Healthy rollout | v1 passed candidate and stable checks and became active. |
| Pre-switch verification failure | The deliberately invalid v2 candidate failed before traffic switching; the stable v1 Endpoint did not change. |
| Endpoint switch failure | Caddy was deliberately stopped for the v2 switch. Provision recorded uncertainty, Caddy was restored, and explicit resume completed the operation. |
| Post-switch failure | The `fail-stable` candidate passed direct checks, failed through the stable Endpoint, and automatically restored the directly verified v2 Generation. |
| Process interruption | The CLI was deliberately killed after switching to v3. The journal retained the intent, and resume proved the already-completed switch without replaying it. |
| Stale executor | An isolated old lineage submitted fencing token `1` after the host had accepted token `30`. The executor rejected it as stale, its isolated journal recorded `op-05` as uncertain, and stable traffic remained on v3. |

The first aggregate run exposed an acceptance-harness ordering defect after the first five scenarios: it attempted to preview the stale lineage only after v3 was active, so planning correctly rejected the already-listening candidate port. The live stale-fence check was then completed against a copy of the durable State Backend by selecting the previously approved v1 Plan and lowering only the copied backend's lease token. The authoritative Deployment database and host were not altered by that setup. The harness was corrected to preview and approve the isolated stale lineage while the host is empty, reconcile its already-proved preparation steps later, and submit the still-pending Endpoint switch under the stale fence.

The rejected request reported:

```text
provision-host-executor: authorization fencing token is stale; outcome recorded as uncertain
```

The isolated journal's latest `op-05` event was `outcome: uncertain`. A subsequent stable request returned `{"revision":"provision-example-http-v3"}`.

## Final host invariants

Independent bootstrap inspection reported `ready: true`, active Revision `provision-example-http-v3`, and previous Revision `provision-example-http-v2`. Caddy was active.

The only Provision application units were:

- `provision-lab-web-4e775436b605.service`
- `provision-lab-web-b6f188a9b2f5.service`
- `provision-lab-web-bac304a88517.service`
- `provision-lab-web-f5d67ce429e0.service`

The only Provision release directories were the four corresponding v1, v2, fail-stable, and v3 Generations. `/srv/gimme` and `gimme-*` units remained absent.

## Qualification boundary

This run qualifies the direct-local native HTTP behavior listed in the [support matrix](../SUPPORT_MATRIX.md). It does not qualify WebSocket draining, retention cleanup, remote SSH transport, another OS/dependency combination, or non-HTTP component roles.
