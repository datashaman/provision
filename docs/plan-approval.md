# Plan approval and status

The initial local State Backend is a versioned SQLite database behind Provision's State Backend interface. It stores each canonical Plan unchanged, including its configuration and observation digests, selected capability evidence, exact operation inputs, approval requirements, and sensitive-value references. Approval decisions are append-only records with an actor, decision time, and expiry. Approval policy is evaluated outside the SQLite adapter.

Persisting a preview makes it the current Plan head for its Environment. This immediately supersedes an older Plan, whether or not the new Plan is approved. Approval always recompiles the supplied configuration and re-observes the Host Target before opening the State Backend. If the fresh Plan identity differs from the requested digest, approval fails without changing the database. Approval also fails if that exact Plan was not persisted by preview or is no longer the current head.

## Approve the previewed Plan

Create a private directory for local state, preview the Plan, and copy its `id` into the approval command:

```sh
mkdir -m 0700 .provision
go run ./cmd/provision plan preview \
  --file examples/host-http/root.yaml \
  --state .provision/state.db
go run ./cmd/provision plan approve \
  --file examples/host-http/root.yaml \
  --plan sha256:PLAN_DIGEST \
  --actor ACTOR \
  --state .provision/state.db \
  --expires-after 15m
```

The State Backend file is created with mode `0600` and later opens reject weaker permissions; its parent directory must already exist. In this local-first slice, the operating-system account authenticates the actor and owner-only access to the State Backend supplies the external `approve` Operation Capability grant. Each grant is scoped to the exact Plan's Application and Environment, and the Plan's approval requirements are evaluated before the decision is recorded. `--actor` must match that authenticated OS username; it cannot assign an arbitrary identity. A future remote coordination boundary will replace this local authority adapter while retaining the same capability-based approval policy.

## Inspect approval and eligibility

```sh
go run ./cmd/provision plan status \
  --plan sha256:PLAN_DIGEST \
  --state .provision/state.db
```

Status opens an existing backend and applies any supported schema migration before reading it. It reports the immutable Plan, approval actor and times, decision, and eligibility. A persisted preview begins as `unapproved`. An approval is eligible only while it is approved, unexpired, and still the Environment's current Plan head. Persisting a different preview for that Environment marks the older Plan `superseded` without deleting either its Plan bytes or approval history.

Eligibility is necessary but is not itself host execution authorization. Execution acquires a fenced Environment lease, journals its intent, and sends a separate short-lived signed authorization for one exact operation to the restricted host executor. See [Authorized release preparation](release-preparation.md).
