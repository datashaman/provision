# Base host remote SSH failure matrix evidence — 2026-09-25

Issue #13 was exercised from a separate macOS controller against the disposable `base` host after a fresh Ubuntu Server installation and Provision bootstrap. The acceptance controller binary was built from commit `6598d23f0182d858713da1effb1462abee662b5c`; the remote executor was built from the same source. The checked-in harness completed all six scenarios in one run and printed `remote SSH failure matrix passed`.

Provision, the authority private key, approvals, and the SQLite State Backend remained on the controller. The remote host received only strict-SSH observations and signed, typed operations through its restricted one-shot executor.

## Observed combination

- Controller: macOS, Darwin `24.6.0`, `arm64`
- Provision binary: `sha256:caff13cd97adcfde3ea9b6573a59f8e601480cebd015f47c4520b7c1b4dfa349`
- Remote host: Ubuntu Server `26.04`, Linux `7.0.0-34-generic`, `x86_64`
- systemd: `259 (259.5-0ubuntu3.4)`
- Caddy: `2.6.2`
- SSH server: `OpenSSH_10.2p1 Ubuntu-2ubuntu3.6, OpenSSL 3.5.5 27 Jan 2026`
- restricted host executor: `sha256:e9c910de10f89045c98d426454c60a3e7e467e3ed7c12c6994bda91bc93f6c38`
- SSH host fingerprint: `SHA256:E6M0uy67VlvnLBWjZhP+Ltk6v7wDS1yYo3eyqBOut6Y`

The host fingerprint was verified out of band after the reset and then pinned in the controller's OpenSSH `known_hosts`. The harness required batch mode, public-key authentication, and strict host-key checking. It neither accepted nor replaced host keys.

## Results

| Scenario | Observation |
| --- | --- |
| Healthy rollout after in-flight SSH disconnect and executor restart | The initial Artifact-stage executor was stopped in flight, its SSH client was killed, and the executor was then killed. Provision recorded `uncertain`, the stable Endpoint remained absent, and resume used a fresh SSH connection and executor before v1 passed candidate and stable checks and became active. |
| Pre-switch verification failure | The deliberately invalid v2 candidate failed and was cleaned up before traffic switching; the stable v1 Endpoint did not change. |
| Endpoint switch failure | Caddy was deliberately stopped after scheduling its automatic restoration. Provision recorded the switch outcome as uncertain rather than successful. After Caddy returned, explicit resume observed the unchanged v1 route and completed the v2 switch. |
| Post-switch failure with lost response | The `fail-stable` candidate switched, failed stable verification, and the remote executor restored the directly verified v2 Generation. The test transport withheld that completed result and killed the controller process. A new Provision process and fresh remote executor reconstructed the failed-with-rollback result from host evidence without replaying rollback. |
| Switch interruption | The remote executor completed the v3 switch, but the test transport withheld the response and killed the controller process. Resume proved the already-completed switch from fresh remote evidence without dispatching it again. |
| Stale executor | An isolated older lineage reached the remote executor after the accepted host fence had advanced. The executor rejected the stale authorization, and stable traffic remained on v3. |

The initial SSH disconnect and remote-executor termination were the intended in-flight fault. The two later shell `Killed: 9` diagnostics were the separate completed-response-loss mechanism. The remote workloads and executor installation were not killed or replaced; the first executor process was deliberately killed, and recovery used a new restricted executor process over a new SSH connection.

The first run of the new in-flight fault exposed an abandoned attempt-named temporary Artifact after the executor was killed. The harness rejected that extra cache entry even though resume had otherwise completed. Commit `6598d23f0182d858713da1effb1462abee662b5c` fixed the executor to remove only residue whose exact attempt identity is proven by the same Environment's root-owned authorization records. A regression test also proves that an unrecorded temporary entry is not swept. The definitive reset-host run contained only the four expected digest-addressed Artifacts.

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
