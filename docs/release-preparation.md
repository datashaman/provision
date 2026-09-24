# Authorized release preparation

Provision can execute the first operation of an approved Host Plan on either the current machine or a bootstrapped remote machine over SSH. That operation is intentionally narrow: `stageArtifact` downloads the Artifact URL already recorded in the Plan, verifies its SHA-256 digest, and stores it at `/var/lib/provision/artifacts/sha256/<digest>`. It does not unpack, install, start, stop, or route the application.

Local and remote execution use the same Plan-bound authorization, fencing, journal, operation envelope, root-owned executor, and structured result. SSH is only the transport boundary; it does not create a second execution model or grant shell-shaped deployment authority.

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

Before contacting the executor, Provision atomically confirms that the Plan is still current and approved, acquires a renewable fenced execution lease for the Environment mutation, assigns a higher fencing token, and journals the intent. This execution lease is not the product's optional team-member **Environment Lease**. It then signs a maximum-five-minute one-use proof for the exact typed operation and observed target. The executor accepts only the fixed `stageArtifact` schema and fixed cache root. It rejects a changed payload, wrong key, wrong executor, wrong Environment or operator, expired proof, replayed attempt, or stale fencing token.

If the digest-addressed Artifact is already present and valid, execution succeeds as `already-present`; otherwise the target executor downloads it over HTTPS to a temporary file, bounds it to 512 MiB, verifies it, and commits it without overwriting another cache entry. A known host failure is returned and journaled with its observation. After an interrupted or unverifiable SSH response, Provision reconnects only to observe the digest-addressed cache. If that observation proves the Artifact is present, it commits success. If the target cannot be observed or the cache state is ambiguous, it records `uncertain` with the observed state and declared recovery mode. It does not blindly retry the mutation or infer failure from a lost connection.

## 5. Read the durable journal

```sh
go run ./cmd/provision deployment status \
  --plan sha256:PLAN_DIGEST \
  --state .provision/state.db
```

The output contains append-only authoritative intent and outcome events, including the attempt identity and fencing token. It survives process restart. The State Backend rejects a concurrent Environment mutation and refuses a late result from an expired or replaced execution lease. A stale token cannot append to that journal, advance state, or release the current lease. Its submitted observation is retained separately under `rejectedResults` as non-authoritative audit evidence so the interrupted history does not disappear.

## Current boundary

This is release preparation, not a release deployment. Later Plan operations remain unavailable: Provision cannot yet create a Generation, install the Artifact, start or verify a candidate, switch an Endpoint, drain, or retain the previous Generation. These omissions are explicit capability failures rather than silent fallback behavior.
