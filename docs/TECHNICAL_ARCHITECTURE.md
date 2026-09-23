# Technical architecture

**Status: Discovery.** Foundational architecture decisions are agreed; further implementation decisions remain open.

## Inputs

Architecture must satisfy:

- the agreed product model in `docs/PRODUCT_MODEL.md`;
- the canonical domain language in `CONTEXT.md`;
- the decisions recorded in `docs/adr/`.

Product constraints are not implementation defaults. If an architecture cannot satisfy one, the conflict must return to product design as an explicit decision rather than being silently weakened.

## Architectural constraints already imposed by the product

- Core local and remote-host workflows cannot require a hosted control plane.
- Deployed workloads continue operating when Provision is unavailable.
- Configuration is declarative and may reference bounded custom Actions.
- Plans are immutable and tied to exact revisions, observed state, capabilities, and artifact digests.
- Recorded operational state is distinct from declared intent.
- Operations are resumable and auditable but are not presented as distributed transactions.
- Implementations expose explicit capability contracts and cannot silently degrade required guarantees.
- The first release targets current or remote Linux systemd hosts and AWS.
- The seven required deployment scenarios must execute and be verified end to end.

## Decisions

### Local-first engine

Provision's behavioral core is a local engine invoked by a CLI. Basic current-host, remote-host, and AWS workflows require neither a resident daemon nor a hosted account. An optional coordinator and remote runner may later provide shared state, approvals, automation, and delegated execution, but they call the same engine rather than reimplementing planning or execution.

### Configuration documents

Users author versioned YAML documents under a restricted YAML 1.2 profile and strict schema. Equivalent JSON is accepted for generated input. Unknown fields are errors, includes are explicit, and parsing produces a canonical internal model with source provenance. Configuration does not execute code; it references immutable Actions.

### Deep core modules

The initial architecture has three primary deep modules:

1. **Configuration Compiler** — documents to a validated canonical model with provenance.
2. **Planner** — canonical desired model, observed state, and capability contracts to an immutable Plan.
3. **Executor** — approved Plan, execution journal, and implementation adapters to a resumable result.

The CLI, future coordinator, automation, and tests use these same interfaces. Provider and runtime variation sits behind adapter seams rather than leaking into callers.

### Plans are canonical data

A Plan is canonical serializable data containing typed operations, dependencies, preconditions, approvals, expected observations, sensitive-value references, recovery behavior, and deterministic identity. Custom Actions are referenced by digest and contract. Plans do not embed arbitrary closures or generated executable scripts.

### Go implementation

The local engine, CLI, and future runner are implemented in Go. Go supports the required desktop and Linux distribution targets, provides the official AWS SDK, and suits a single distributable operational binary with typed internal models and explicit concurrency.

### Journaled state behind a State Backend seam

Each environment has an append-only execution journal and immutable state snapshots for efficient reads. A deep State Backend module hides persistence and exposes a small interface for loading the current snapshot, conditionally appending journal entries, acquiring or renewing a fenced lease, storing immutable Plans and evidence, and compare-and-swapping the active snapshot.

The initial architecture includes two adapters because this seam has real variation: SQLite for personal or local environments and an AWS-backed remote adapter for shared environments. Exact AWS storage services remain undecided.

### Fenced environment leases

Only one mutating operation may execute against an environment at a time. A renewable lease carries a monotonically increasing fencing token; every conditional journal append verifies the token. Reads and planning may occur concurrently, but execution revalidates state after acquiring the lease. Expired work may be resumed or explicitly superseded, while a stale executor cannot commit further progress.

### Explicit push operations

Environment changes follow an explicit observe, plan, approve, execute flow. Provision has no universal pull controller and does not silently reconcile drift. Interrupted operations resume from their journal and fresh provider observations rather than assuming the initiating process remained alive. Narrow reconciliation may be opted into later but still emits recorded operations.

### Agentless host transport

One host interface has a direct local adapter for the current Linux machine and an agentless SSH adapter for remote Linux hosts. The remote adapter transfers digest-addressed artifacts, installs versioned releases, manages systemd and supporting services, and observes health. It does not require a resident Provision agent; reconnect and resume use recorded checkpoints and observed host state.
