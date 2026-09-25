# Direct-local failure matrix

The direct-local acceptance harness exercises the current HTTP host Deployment on an already bootstrapped disposable Linux machine. It drives the same `provision` binary and restricted host executor used by a normal Deployment; the faults are introduced only at declared operation boundaries.

The harness is destructive to Provision-owned resources for its Environment. Run it only on a freshly reset and bootstrapped host whose hostname you explicitly confirm. It refuses a host with an existing stable Endpoint, Provision application units, Provision release directories, or Gimme-owned resources.

## Run it

Copy a Linux `provision` binary, the repository checkout, and the matching authority private key to the host. Then, as the bootstrapped non-root operator, run:

```sh
./scripts/test-direct-local-host.sh \
  --provision ./provision \
  --signing-key ./host-authority.key \
  --work-dir /tmp/provision-direct-local-evidence \
  --provision-version GIT_COMMIT_OR_RELEASE \
  --confirm-disposable-host "$(hostname)"
```

The work directory must not exist before the run. The script obtains one `sudo` credential for its controlled Caddy outage and otherwise reaches privileged host mutation only through the restricted executor.

## Scenarios

| Scenario | Injected boundary | Required result |
| --- | --- | --- |
| Healthy rollout | none | Candidate becomes active and healthy through the stable Endpoint. |
| Pre-switch failure | candidate verification | Failed candidate is removed; the stable Endpoint and active Generation do not change. |
| Endpoint switch failure | Caddy unavailable during `switchEndpoint` | Outcome is uncertain, Caddy is restored, explicit resume completes the switch, and the exact drained previous Generation is retained through its declared deadline. |
| Bounded HTTP drain and retention | A one-second ordinary request spans the Caddy handoff; the CLI loses both the completed `drainPrevious` response and, separately, the completed `retainPrevious` response | The request completes on the previous revision within the declared bound; only that previous unit stops; its unit, release, manifest, and Artifact remain restartable until exactly switch time plus the declared rollback window; each resume proves durable completion from fresh evidence without replay. |
| Post-switch failure | stable-only health failure | Exact retained previous Generation is verified and restored; outcome remains failed with proved rollback. |
| Process interruption | CLI killed after Caddy switched but before the result was committed | Journal retains intent; resume observes the completed switch and does not replay it. |
| Stale executor | separately authorized old Plan attempts to record its own candidate verification after the main lineage advanced the host fence | Executor rejects the stale fencing token; the isolated lineage records an uncertain outcome and stable traffic does not move. |

The shell's `Killed` diagnostics in the drain/retention and interruption scenarios are expected evidence of the injected process terminations. They are not failed assertions.

Every scenario checks the stable revision and the latest journal state. The final checks also require the expected active and previous identities, the exact Provision-owned unit and release sets, active Caddy, and the continued absence of `/srv/gimme` and `gimme-*` units. Drain status is explicitly `mode: bounded-http`. Retention status is explicitly `retained` under `policy: rollback-window`, with no cleanup and a deadline derived from the trusted switch time. The harness does not qualify WebSocket or other long-lived streams.

## Evidence

On success, the harness prints `direct-local failure matrix passed` and leaves all JSON, command output, expected/actual host inventories, retained-file checksums, and `support-observation.json` in the work directory. The structured support observation records the supplied Provision source revision, exact binary and executor digests, and observed OS, kernel, architecture, systemd, and Caddy versions. Preserve that directory until the summarized evidence and [support matrix](SUPPORT_MATRIX.md) have been updated.

The harness deliberately does not reset, bootstrap, or clean the machine. Those lifecycle boundaries remain explicit operator actions.
