# Base host remote SSH failure matrix evidence — 2026-09-25

Issue #13 was exercised from a separate macOS controller against the disposable `base` host after a fresh Ubuntu Server installation and Provision bootstrap. The acceptance controller binary was built from commit `051460d7040ad6bae5cc0ac0c33ed2ef45daccd6`; the remote executor was built from the same source. The checked-in harness completed all six scenarios in one run and printed `remote SSH failure matrix passed`.

Provision, the authority private key, approvals, and the SQLite State Backend remained on the controller. The remote host received only strict-SSH observations and signed, typed operations through its restricted one-shot executor.

## Observed combination

- Controller: macOS, Darwin `24.6.0`, `arm64`
- Provision binary: `sha256:f467d84db5031f990a99770a8f20ed88afa08c35eeccfc78be8d1c02677eb862`
- Remote host: Ubuntu Server `26.04`, Linux `7.0.0-34-generic`, `x86_64`
- systemd: `259 (259.5-0ubuntu3.4)`
- Caddy: `2.6.2`
- SSH server: `OpenSSH_10.2p1 Ubuntu-2ubuntu3.6, OpenSSL 3.5.5 27 Jan 2026`
- restricted host executor: `sha256:d3e958df7d6814df3510ad875f3c5e53e7ad82936e4aaf9f01ec263916110f77`
- SSH host fingerprint: `SHA256:Wncb+SL2zJfiTjCVKeOVuYqJIZ3zC9yhIfPiR2B9qIo`

The host fingerprint was verified out of band after the reset and then pinned in the controller's OpenSSH `known_hosts`. The harness required batch mode, public-key authentication, and strict host-key checking. It neither accepted nor replaced host keys.

## Results

| Scenario | Observation |
| --- | --- |
| Healthy rollout | Remote v1 passed candidate and stable checks and became active. |
| Pre-switch verification failure | The deliberately invalid v2 candidate failed and was cleaned up before traffic switching; the stable v1 Endpoint did not change. |
| Endpoint switch failure | Caddy was deliberately stopped after scheduling its automatic restoration. Provision recorded the switch outcome as uncertain rather than successful. After Caddy returned, explicit resume observed the unchanged v1 route and completed the v2 switch. |
| Post-switch failure with lost response | The `fail-stable` candidate switched, failed stable verification, and the remote executor restored the directly verified v2 Generation. The test transport withheld that completed result and killed the controller process. A new Provision process and fresh remote executor reconstructed the failed-with-rollback result from host evidence without replaying rollback. |
| Switch interruption | The remote executor completed the v3 switch, but the test transport withheld the response and killed the controller process. Resume proved the already-completed switch from fresh remote evidence without dispatching it again. |
| Stale executor | An isolated older lineage reached the remote executor after the accepted host fence had advanced. The executor rejected the stale authorization, and stable traffic remained on v3. |

The two shell `Killed: 9` diagnostics were the intended controller-side interruption mechanism. The remote workloads and executor installation were not killed or replaced. Each later observation or operation used a new restricted executor process over a new SSH connection.

## Final host invariants

The harness compared the stable Endpoint, active and previous Generation identities, journal result, exact Artifact/unit/release inventories, cached Artifact digests, retained-file checksums, and the Caddy configuration outside the one Plan-owned server after every scenario. All checks passed.

The final inventories contained exactly four Provision Artifacts, four application units, and four immutable release directories, corresponding to v1, v2, `fail-stable`, and v3. A separate post-run bootstrap inspection reported `ready: true`, v3 active through the stable Endpoint, and v2 retained as the previous Generation. Caddy remained active. `/srv/gimme` and `gimme-*` units remained absent.

The stale request reported:

```text
provision: remote Host Target operation failed: exit status 1: provision-host-executor: authorization fencing token is stale; outcome recorded as uncertain
```

A subsequent stable request returned `{"revision":"provision-example-http-v3"}`.

## Parity and qualification boundary

For the implemented six-operation native HTTP lifecycle, the remote run demonstrated the same candidate isolation, switch eligibility, stable-route activation, bounded rollback, interruption recovery, fencing, journal, and ownership guarantees as the direct-local run.

SSH adds a management-transport failure boundary. Loss of access does not stop the independently managed workload or change the stable route, but it can prevent Provision from proving an operation's outcome. In that case the journal remains interrupted or uncertain until access is restored and the operator resumes. A changed or unknown host key fails before the executor is invoked and requires separate out-of-band verification; Provision does not rotate trust automatically.

This run does not qualify WebSocket draining, retention cleanup, OCI/Podman, EC2, ECS, Lambda, workers, schedules, databases, key-value stores, realtime services, another controller/host version combination, or unattended host reset and bootstrap.
