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
