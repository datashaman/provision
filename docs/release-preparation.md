# Authorized release preparation

Provision can execute the first operation of an approved Host Plan on the current machine. That operation is intentionally narrow: `stageArtifact` downloads the Artifact URL already recorded in the Plan, verifies its SHA-256 digest, and stores it at `/var/lib/provision/artifacts/sha256/<digest>`. It does not unpack, install, start, stop, or route the application.

This slice is for a direct-local Host Target. The machine running `provision deployment execute` must be the bootstrapped target, although it may be reached interactively from another machine first. A configured remote address is rejected. SSH execution will reuse this authorization contract in the next host slice.

## 1. Create the Environment authority

Create the key pair on the operator side:

```sh
mkdir -m 0700 .provision
go run ./cmd/provision authority keygen \
  --private-key .provision/host-authority.key \
  --public-key .provision/host-authority.pub
```

The command refuses to overwrite either file. Keep the private key owner-only and off the target's root-owned verification directories. Bootstrap installs only the public key; follow the [host bootstrap guide](host-bootstrap.md), including its `--authority-public-key` argument.

## 2. Declare the target as local

For the Environment being exercised, the selected Host Target must identify the current machine and the already-bootstrapped operator:

```yaml
targets:
  current:
    kind: host
    local: true
    user: OPERATOR
```

Do not set `address` on a local target. Inspection still goes through the fixed root-owned executor via the operator's restricted `sudo` rule. The resulting Plan records the executor digest and authority-key identity observed on that machine.

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

Before contacting the executor, Provision atomically confirms that the Plan is still current and approved, acquires the Environment's lease, assigns a higher fencing token, and journals the intent. It then signs a maximum-five-minute one-use proof for the exact typed operation and observed local target. The executor accepts only the fixed `stageArtifact` schema and fixed cache root. It rejects a changed payload, wrong key, wrong executor, wrong Environment or operator, expired proof, replayed attempt, or stale fencing token.

If the digest-addressed Artifact is already present and valid, execution succeeds as `already-present`; otherwise it is downloaded over HTTPS to a temporary file, bounded to 512 MiB, verified, and committed without overwriting another cache entry. A failed or unverifiable executor response is recorded as `uncertain` when the lease still permits that durable commit. Do not infer failure from an uncertain result; inspect the journal and host state before retry or recovery work.

## 5. Read the durable journal

```sh
go run ./cmd/provision deployment status \
  --plan sha256:PLAN_DIGEST \
  --state .provision/state.db
```

The output contains append-only intent and outcome events, including the attempt identity and fencing token. It survives process restart. The State Backend rejects a concurrent Environment mutation and refuses a late result from an expired or replaced lease.

## Current boundary

This is release preparation, not a release deployment. Later Plan operations remain unavailable: Provision cannot yet create a Generation, install the Artifact, start or verify a candidate, switch an Endpoint, drain, or retain the previous Generation. The remote-host transport is also unavailable in this slice. These omissions are explicit capability failures rather than silent fallback behavior.
