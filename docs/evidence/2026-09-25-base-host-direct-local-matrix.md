# Base host direct-local failure matrix evidence — 2026-09-25

Issue #12 was exercised on the disposable `base` host after a fresh Ubuntu Server installation and Provision bootstrap. The definitive acceptance binary was built from commit `b4ee92789c242056b5fbe9a51ab42b8c5d02bc36` for Linux `x86_64`. The checked-in harness completed all six scenarios in one run and printed `direct-local failure matrix passed`.

## Observed platform

- Ubuntu Server `26.04`
- Linux `7.0.0-34-generic`
- systemd `259 (259.5-0ubuntu3.4)`
- Caddy `2.6.2`
- Provision binary: `sha256:d4baa0bf57f3620ae1e473a748391eb55700679331267762087ad1e3f2c78229`
- restricted host executor: `sha256:c532f3b78d291c400761b187c1fe8c14e84f1427b9d59498d71d1f73e6f0b21e`
- SSH host fingerprint: `SHA256:yGhpFfqNs6IGDQoTCJr6j3ZP31wXlOLWFX77UjZ6Z7A`

Bootstrap inspection was ready before the run. The initial host had no Provision Deployment state, stable Endpoint, Provision application units, Provision releases, `/srv/gimme`, or `gimme-*` units.

## Results

| Scenario | Observation |
| --- | --- |
| Healthy rollout | v1 passed candidate and stable checks and became active. |
| Pre-switch verification failure | The deliberately invalid v2 candidate failed before traffic switching; the stable v1 Endpoint did not change. |
| Endpoint switch failure | Caddy was deliberately stopped for the v2 switch. Provision recorded uncertainty, Caddy was restored, and explicit resume completed the operation. |
| Post-switch failure | The `fail-stable` candidate passed direct checks, failed through the stable Endpoint, and automatically restored the directly verified v2 Generation. |
| Process interruption | The CLI was deliberately killed after switching to v3. The journal retained the intent, and resume proved the already-completed switch without replaying it. |
| Stale executor | An isolated old lineage submitted fencing token `4` after the host had accepted token `30`. The executor rejected it as stale, its isolated journal recorded `op-04` as uncertain, and stable traffic remained on v3. |

The first aggregate run exposed an acceptance-harness ordering defect after the first five scenarios: it attempted to preview the stale lineage only after v3 was active, so planning correctly rejected the already-listening candidate port. The live stale-fence check was then completed against a copy of the durable State Backend by selecting the previously approved v1 Plan and lowering only the copied backend's lease token. The authoritative Deployment database and host were not altered by that setup.

A second reset-host run previewed and approved the isolated stale lineage while the host was empty. Its Artifact, Generation, and running candidate were later re-observed as satisfied under tokens 1 through 3. Candidate health was also observable, but the stale Plan did not own the baseline Plan's host-side verification record; recording its own exact verification was therefore the first required host mutation. The executor rejected token 4 as stale, the isolated journal recorded `op-04` as uncertain, and stable traffic remained on v3. The harness now expects the rejection at that exact boundary.

The definitive third reset-host run used that checked-in boundary and completed without intervention beyond the initial `sudo` authentication. Its structured support observation recorded the source revision, binary and executor digests, OS, kernel, architecture, systemd, and Caddy versions together with `result: passed` and all six matrix labels.

The rejected request reported:

```text
provision-host-executor: authorization fencing token is stale; outcome recorded as uncertain
```

The definitive run's isolated journal retained successful outcomes for `op-01` through `op-03`, followed by `op-04` as `outcome: uncertain`. A subsequent stable request returned `{"revision":"provision-example-http-v3"}`.

## Final host invariants

Independent bootstrap inspection reported `ready: true`, active Revision `provision-example-http-v3`, and previous Revision `provision-example-http-v2`. Caddy was active.

The only Provision application units were:

- `provision-lab-web-4e775436b605.service`
- `provision-lab-web-b6f188a9b2f5.service`
- `provision-lab-web-bac304a88517.service`
- `provision-lab-web-f5d67ce429e0.service`

The only Provision release directories were the four corresponding v1, v2, fail-stable, and v3 Generations. `/srv/gimme` and `gimme-*` units remained absent.

After every scenario, the harness also compared the exact Artifact, systemd-unit, and release-directory sets; verified every cached Artifact digest; proved previously retained unit, Artifact, and release files unchanged; and compared the complete Caddy configuration outside the single Plan-owned server with its pre-Deployment snapshot.

## Qualification boundary

This run qualifies the direct-local native HTTP behavior listed in the [support matrix](../SUPPORT_MATRIX.md). It does not qualify WebSocket draining, retention cleanup, remote SSH transport, another OS/dependency combination, or non-HTTP component roles.
