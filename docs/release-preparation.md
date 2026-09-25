# Authorized release preparation

Provision can execute the first eight operations of an approved Host Plan on either the current machine or a bootstrapped remote machine over SSH. `stageArtifact` downloads the Artifact URL already recorded in the Plan, verifies its SHA-256 digest, and stores it at `/var/lib/provision/artifacts/sha256/<digest>`. Dependency-gated operations then install that exact native bundle as an immutable candidate Generation, start a separate hardened systemd unit on a loopback-only port, evaluate the declared liveness, readiness, and revision-bound candidate-verification checks, atomically route the stable Endpoint to the verified candidate through Caddy, evaluate the same Health Contract through that stable Endpoint, complete its bounded ordinary-HTTP drain, and declare the exact stopped previous Generation restartable through the Environment's rollback-window deadline.

Local and remote execution use the same Plan-bound authorization, fencing, journal, operation envelope, root-owned executor, and structured result. SSH is only the transport boundary; it does not create a second execution model or grant shell-shaped deployment authority.

The first complete reset-host exercise, including both deliberate verification failure and successful switch eligibility without endpoint activation, is recorded in [the 2026-09-24 candidate preparation evidence](evidence/2026-09-24-base-host-candidate.md). The subsequent stable-route activation, retained previous Generation, in-flight HTTP drain, and Caddy restart exercise is recorded in [the 2026-09-25 Endpoint switch evidence](evidence/2026-09-25-base-host-endpoint-switch.md). Stable-route verification and automatic recovery from a reproducible post-switch failure are recorded in [the 2026-09-25 rollback evidence](evidence/2026-09-25-base-host-post-switch-rollback.md). Process interruption after both the traffic switch and automatic rollback, followed by journal-driven resumption, is recorded in [the 2026-09-25 interrupted Deployment evidence](evidence/2026-09-25-base-host-interrupted-resume.md). The repeatable healthy and fault-injection sequence is described in the [direct-local failure matrix](direct-local-failure-matrix.md), with its combined host run recorded in [the 2026-09-25 direct-local matrix evidence](evidence/2026-09-25-base-host-direct-local-matrix.md). Bounded drain plus exact rollback-window recording and observation-only recovery are recorded in [the 2026-09-25 rollback-window evidence](evidence/2026-09-25-base-host-rollback-window.md). The equivalent earlier sequence from a separate macOS controller is described in the [remote SSH failure matrix](remote-ssh-failure-matrix.md) and recorded in [the 2026-09-25 remote evidence](evidence/2026-09-25-base-host-remote-ssh-matrix.md). Tested combinations and their qualification boundaries appear in the [test-backed support matrix](SUPPORT_MATRIX.md).

## 1. Create the Environment authority

Create the key pair on the operator side:

```sh
mkdir -m 0700 .provision
go run ./cmd/provision authority keygen \
  --private-key .provision/host-authority.key \
  --public-key .provision/host-authority.pub
```

The command refuses to overwrite either file. Keep the private key owner-only and off the target's root-owned verification directories. Bootstrap installs only the public key; follow the [host bootstrap guide](host-bootstrap.md), including its `--authority-public-key` argument.

## 2. Declare a local or remote target

For the Environment being exercised, the selected Host Target must identify the current machine and the already-bootstrapped operator:

```yaml
targets:
  current:
    kind: host
    local: true
    user: OPERATOR
```

Do not set `address` on a local target. Inspection still goes through the fixed root-owned executor via the operator's restricted `sudo` rule. The resulting Plan records the executor digest and authority-key identity observed on that machine.

For a remote target, declare the already-bootstrapped machine and SSH user instead:

```yaml
targets:
  current:
    kind: host
    address: 192.168.101.109
    user: OPERATOR
```

The machine's SSH key must already be trusted by the operator account, normally through its OpenSSH `known_hosts` file. Provision uses batch mode, disables password authentication, enables strict host-key checking, and never accepts or replaces a host key automatically. An unknown or changed host identity therefore fails before the remote executor is invoked. The inspector also records the host's own ED25519 fingerprint in the Plan; the signed operation binds it, and the executor recomputes and verifies it before accepting a remote-target envelope. Pin and verify the fingerprint out of band when preparing a reset machine.

## 3. Preview and approve the exact Plan

```sh
go run ./cmd/provision plan preview \
  --file PATH/TO/root.yaml \
  --state .provision/state.db

go run ./cmd/provision plan approve \
  --file PATH/TO/root.yaml \
  --plan sha256:PLAN_DIGEST \
  --actor "$(id -un)" \
  --state .provision/state.db \
  --expires-after 15m
```

Use the `id` emitted by preview verbatim. Approval re-observes the target and fails if configuration or host evidence now produces a different Plan.

## 4. Execute Artifact preparation

The first operation in the current host Plan is `op-01`:

```sh
go run ./cmd/provision deployment execute \
  --plan sha256:PLAN_DIGEST \
  --operation op-01 \
  --state .provision/state.db \
  --signing-key .provision/host-authority.key
```

Before contacting the executor, Provision atomically confirms that the Plan is still current and approved, acquires a renewable fenced execution lease for the Environment mutation, assigns a higher fencing token, and journals the intent. This execution lease is not the product's optional team-member **Environment Lease**. It then signs a maximum-five-minute one-use proof for the exact typed operation and observed target. The executor accepts only the enabled typed schemas and fixed host paths. It rejects a changed payload, wrong key, wrong executor, wrong Environment or operator, expired proof, replayed attempt, or stale fencing token.

If the digest-addressed Artifact is already present and valid, execution succeeds as `already-present`; otherwise the target executor downloads it over HTTPS to a temporary file, bounds it to 512 MiB, verifies it, and commits it without overwriting another cache entry. A known host failure is returned and journaled with its observation. After an interrupted or unverifiable SSH response, Provision reconnects only to observe the digest-addressed cache. If that observation proves the Artifact is present, it commits success. If the target cannot be observed or the cache state is ambiguous, it records `uncertain` with the observed state and declared recovery mode. It does not blindly retry the mutation or infer failure from a lost connection.

## 5. Install, start, and verify the candidate

Execute the next operations in order with the same Plan, State Backend, and signing key:

```sh
for operation in op-02 op-03 op-04; do
  go run ./cmd/provision deployment execute \
    --plan sha256:PLAN_DIGEST \
    --operation "$operation" \
    --state .provision/state.db \
    --signing-key .provision/host-authority.key
done
```

The State Backend refuses an operation until every dependency has a latest successful journal outcome. `op-02` re-verifies the cached Artifact digest, accepts only a gzip-compressed tar bundle containing exactly one executable regular file at its root, and atomically records a Generation manifest binding the Revision, Artifact digest, Environment account, and fixed release directory. The root-owned release root, Generation directory, manifest, and executable are checked for unsafe links, ownership, and permissions.

`op-03` writes only the exact Plan-bound unit under `/etc/systemd/system`, runs it as the dedicated Environment account, and supplies `PROVISION_HTTP_LISTEN=127.0.0.1:<candidate-port>` plus `PROVISION_REVISION=<revision>`. A start failure removes only the new candidate unit. Cleanup refuses the recorded active Generation and never removes its unit or release directory.

`op-04` first re-observes the exact Plan-bound systemd unit and immutable Generation, including its Artifact digest, and then contacts only that candidate's loopback port. It requires successful liveness and readiness responses and requires the candidate-verification response to report the planned Revision. Only an active matching unit plus all three checks passing produces `switchEligible: true`. A failed check is journaled with the individual check results, stops and removes only the failed candidate unit and Generation, and leaves Caddy, the stable Endpoint, and the active-generation record untouched.

## 6. Switch the stable Endpoint

Execute `op-05` only after `op-04` succeeds:

```sh
go run ./cmd/provision deployment execute \
  --plan sha256:PLAN_DIGEST \
  --operation op-05 \
  --state .provision/state.db \
  --signing-key .provision/host-authority.key
```

The State Backend requires the successful `op-04` outcome, and the host independently requires its own root-owned record of a successful exact candidate verification for the same signed Plan. The executor re-observes the immutable Generation and systemd unit, refuses drift in the planned previous Generation or stable route, and changes only the Plan-owned Caddy server. Caddy provisions the new configuration before unloading the old configuration; ordinary in-flight HTTP requests therefore follow the recorded `caddy-graceful-config-reload` drain policy while new requests use the candidate. The previous unit and release directory remain runnable.

After Caddy reports the exact route and stable port, the executor atomically records the active and previous Generation identities, Plan, verification-operation digest, drain policy, and switch time. A Caddy load failure leaves the previous route in place. If recording the active Generation fails after the route load, the executor restores the prior Caddy server before reporting failure. The structured result and durable journal identify both Generations across Provision process restart. Bootstrap configures Caddy to resume its autosaved active JSON configuration so the same stable route also survives Caddy and host restarts; planning fails closed when that service contract is absent.

This guarantee is intentionally limited to ordinary HTTP requests. Caddy documents separate stream behavior for WebSockets and other long-lived streams, so this HTTP implementation does not claim realtime connection draining. See Caddy's [zero-downtime reload documentation](https://caddyserver.com/docs/getting-started#reloading-config) and [reverse-proxy streaming behavior](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming).

## 7. Verify the switched Endpoint and recover safely

Execute `op-06` only after `op-05` succeeds:

```sh
go run ./cmd/provision deployment execute \
  --plan sha256:PLAN_DIGEST \
  --operation op-06 \
  --state .provision/state.db \
  --signing-key .provision/host-authority.key
```

The executor binds this check to the signed candidate, stable Endpoint, and exact previous Generation from the Plan. It first confirms that the durable active-generation record, candidate unit, and Caddy route still agree with that Plan, then evaluates liveness, readiness, and revision identity through the stable port. A complete successful check records `healthy` without touching either retained unit.

If stable-route health fails, the executor does not immediately move traffic. It first verifies the retained previous Generation directly on its private loopback port. Only a complete healthy result permits Caddy to restore that exact previous upstream. The executor then repeats the complete Health Contract through the stable Endpoint and atomically swaps the durable active and previous identities. The operation is recorded as failed with status `rolled-back`, the original post-switch failure reason, both sets of recovery checks, and the observed restored upstream. This deliberately prevents dependent drain and retention operations from proceeding as though the candidate deployment succeeded.

If there is no signed previous Generation, the previous Generation is unhealthy, Caddy cannot establish the rollback route, stable-route recovery cannot be verified, or durable active state cannot be recorded, the executor returns a structured `uncertain` result with an explicit recovery action. Provision commits that result to the State Backend and requires operator inspection; it never converts ambiguity into success. Both Generations remain retained by this slice.

## 8. Complete the bounded ordinary-HTTP drain

The Environment makes the supported contract explicit:

```yaml
endpoint:
  port: 18080
  drain:
    mode: bounded-http
    maxDuration: 2s
```

`mode` must be `bounded-http`. `maxDuration` must be a canonical Go duration from one second through five minutes. Missing, malformed, noncanonical, shorter, longer, or differently scoped requirements fail configuration validation before a Plan exists.

Execute `op-07` only after `op-06` succeeds:

```sh
go run ./cmd/provision deployment execute \
  --plan sha256:PLAN_DIGEST \
  --operation op-07 \
  --state .provision/state.db \
  --signing-key .provision/host-authority.key
```

The Plan binds the exact active Endpoint, exact previous Generation and unit, `caddy-graceful-config-reload` handoff policy, and declared maximum duration. The host independently proves the successful signed `op-06`, durable active/previous identities, active unit, stable Caddy route, previous unit definition, and immutable Generation directory. Successful `op-06` durably records its signed verification digest and completion time. The full drain bound starts there; `op-07` waits only the portion of that post-verification interval that remains after interruption or restart, re-observes the identities, then stops exactly the previous systemd unit. It does not delete or disable that unit and does not remove the Generation directory.

The result and `deployment status` evidence use `mode: bounded-http` and `status: drained`. They include the operation digest, switch time, deadline, bound completion, stable-route proof, previous-unit state, and retained rollback material. A stopped unit without durable completion is reconciled and recorded under a new signed attempt rather than blindly stopped again. A lost response after durable completion is resolved from fresh observation without redispatch. Drift or ambiguity yields `uncertain` with a recovery action; a proved stop refusal yields `failed`.

This bound is for ordinary finite HTTP requests. It gives requests admitted before Caddy's graceful configuration handoff their declared completion interval before the previous process is stopped. It does not inspect individual requests, migrate connections, or promise WebSocket and other long-lived-stream drain behavior.

## 9. Record rollback-window retention

The Environment declares a canonical rollback window independently of the HTTP drain:

```yaml
rollbackWindow: 30m0s
```

It must be from one minute through 30 days. Execute `op-08` only after `op-07` succeeds:

```sh
go run ./cmd/provision deployment execute \
  --plan sha256:PLAN_DIGEST \
  --operation op-08 \
  --state .provision/state.db \
  --signing-key .provision/host-authority.key
```

The deterministic Plan contains the exact active Endpoint, exact previous Generation, `rollback-window` rule, and duration; it deliberately contains no wall-clock deadline. During execution the host independently proves the successful exact signed drain, stable route, stopped previous unit, immutable Generation directory and manifest, and digest-addressed Artifact. It derives `retainUntil` as the trusted switch time plus the declared window, then records the operation digest and timestamps in root-owned state. The result reports `policy: rollback-window`, `status: retained`, `restartable: true`, and `cleanupPerformed: false`.

This operation records a rollback window; it is not garbage collection. It never starts, stops, deletes, or disables a unit and never removes a Generation directory, manifest, or Artifact. Replays, stale authorizations, mismatched identities, route drift, missing material, and attempts to shorten an existing window fail closed. If the response is lost after the marker is durable, resume re-observes the exact state and records success without replaying the mutation.

## 10. Resume an interrupted or uncertain operation

After the original execution lease has expired or been released, resume the latest interrupted or uncertain operation recorded for the Plan:

```sh
go run ./cmd/provision deployment resume \
  --plan sha256:PLAN_DIGEST \
  --state .provision/state.db \
  --signing-key .provision/host-authority.key
```

Resume selects the operation from the append-only journal; the operator does not re-specify an operation ID. The State Backend atomically requires the selected attempt to remain that operation's latest resumable event, grants a higher fencing token, and records `resumeOfAttemptId` in the new intent. A still-live execution lease must expire before another executor can resume it. A known `failed` outcome is not silently retried: it requires an explicit new execution decision.

The new executor observes the Host Target before causing another side effect. If the intended result is already proved, it records success from that fresh evidence without dispatching the mutation again. If the operation is still pending, its typed idempotent implementation may continue under the new fence. If observation is ambiguous, it records another `uncertain` outcome with the evidence, declared recovery mode, and an actionable `recoveryAction`; no mutation is replayed.

Traffic switching has an additional host-side recovery boundary. If Caddy already points to the signed candidate but the durable active-generation record still names the signed previous Generation, the executor verifies that exact partial state and completes the durable record instead of loading the route again. A route matching neither signed Generation is uncertain. Post-switch verification similarly recognizes a rollback already completed by an interrupted attempt, rechecks the restored Generation directly and through the stable Endpoint, and records the reconstructed rolled-back result.

Every resume handles one journaled operation. Run `deployment status` after it, and invoke resume again only if the latest outcome remains uncertain and the reported recovery action has been completed.

## 11. Read the durable journal

```sh
go run ./cmd/provision deployment status \
  --plan sha256:PLAN_DIGEST \
  --state .provision/state.db
```

The output contains append-only authoritative intent and outcome events, including the attempt identity, resume provenance, fencing token, and structured evidence. It survives process restart. The State Backend rejects a concurrent Environment mutation and refuses a late result from an expired or replaced execution lease. A stale token cannot append to that journal, advance state, or release the current lease. Its submitted observation is retained separately under `rejectedResults` as non-authoritative audit evidence so the interrupted history does not disappear.

## Current boundary

This slice now completes the bounded ordinary-HTTP drain and records the exact previous Generation as restartable through its declared rollback-window deadline. Expiry does not authorize deletion: cleanup, quarantine, and garbage collection remain separate unimplemented policy decisions. WebSocket and other long-lived-stream drain guarantees also remain unsupported rather than silently inferred from ordinary HTTP behavior.
