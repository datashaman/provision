# Remote SSH failure matrix

The remote acceptance harness exercises the complete HTTP Host Deployment lifecycle from a separate controller. Provision, the authority private key, approvals, and the SQLite State Backend remain on the controller; the disposable Linux Host Target receives only strict-SSH observation requests and signed typed operations for its restricted executor.

Run it only against a freshly reset, bootstrapped host whose new ED25519 key has been verified out of band and pinned in the controller's OpenSSH `known_hosts`. The harness uses batch mode, disables password authentication, requires strict host-key checking, and confirms the remote hostname before planning.

## Run it

From the repository checkout on the controller:

```sh
./scripts/test-remote-ssh-host.sh \
  --provision ./provision \
  --signing-key ./.provision/host-authority.key \
  --work-dir /tmp/provision-remote-ssh-evidence \
  --provision-version GIT_COMMIT_OR_RELEASE \
  --target base.local \
  --target-user OPERATOR \
  --confirm-disposable-host base
```

The work directory must not exist. The controller needs `bash`, `curl`, `python3`, and OpenSSH. It may be macOS or Linux; it is not itself the Host Target.

The first scenario uses an external, interactive `sudo systemd-run` fault to stop an in-flight remote executor, signal the controller, and terminate that executor after five seconds. The harness kills only the matching local SSH client while the executor is stopped. Provision must record the operation as uncertain, leave the Endpoint absent, and resume it through a fresh SSH connection and executor process. This is a bounded acceptance-test fault outside Provision's operation model, not a deployment capability or general remote-command feature.

Another scenario deliberately makes Caddy unavailable. Before stopping Caddy, the harness uses the same explicit test boundary to schedule automatic restoration after twenty seconds.

## Scenarios

| Scenario | Remote/transport boundary | Required result |
| --- | --- | --- |
| Healthy rollout after an in-flight SSH disconnect and executor restart | The remote executor is stopped while staging the first Artifact, the active SSH client is killed, and the executor is then killed | The journal records uncertainty rather than success, the Endpoint remains absent, and resume uses a new SSH connection and executor process before completing the healthy rollout. |
| Pre-switch failure | candidate verification | Failed candidate is removed; stable traffic and active identity do not change. |
| Endpoint switch failure | remote Caddy outage | Outcome is uncertain, automatic fault recovery restores Caddy, explicit resume completes the switch, and the recovered Deployment continues through drain and rollback-window retention. |
| Bounded HTTP drain and retention | A one-second ordinary request spans the Caddy handoff; the controller separately loses the completed `drainPrevious` and `retainPrevious` responses | The request completes on the previous Revision within the declared bound; only that previous unit stops; its exact unit definition, Generation directory, manifest, and Artifact remain restartable through switch time plus the declared rollback window; each resume proves durable completion through a fresh SSH connection without replay. |
| Post-switch failure with lost response | SSH returns the completed rollback result to a fault wrapper, which withholds it and kills the controller process | The journal retains intent; a new Provision process re-observes and records the already-completed rollback as failed with recovery proved. |
| Switch interruption | SSH returns the completed switch result to a fault wrapper, which withholds it and kills the controller process | Resume proves the switch from remote evidence without dispatching it again. |
| Stale executor | an independently authorized old lineage reaches the remote executor after the main lineage advanced its host fence | The remote executor rejects the stale token and stable traffic remains unchanged. |

The four controller `Killed` diagnostics for drain, retention, rollback, and switch response loss are expected fault evidence. They do not kill or replace the remote workload or executor. The first scenario separately kills the in-flight SSH client and the deliberately stopped remote executor, then proves recovery through a new one-shot restricted executor over SSH.

After every scenario, the harness checks the stable Endpoint, active and previous identities, journal result, exact remote Artifact/unit/release inventories, retained-file immutability, Caddy ownership boundary, Caddy service state, and continued absence of Gimme paths and units.

## Evidence

Success ends with `remote SSH failure matrix passed`. The work directory contains Plans, approvals, operation output, journals, remote host and Caddy observations, expected/actual inventories, retained-file checksums, and `support-observation.json`. That structured record includes the controller platform, remote OS and service versions, exact Provision and executor digests, SSH server version, pinned host fingerprint, and the completed drain and retention cases. The complete direct-local and SSH qualification is recorded in the [2026-09-25 lifecycle evidence](evidence/2026-09-25-acceptance-vm-complete-http-lifecycle.md).

The harness does not reset or bootstrap the Host Target. Those remain explicit operator-controlled boundaries.
