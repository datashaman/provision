# Technical architecture

**Status: Discovery.** No implementation architecture has been agreed yet.

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

None yet.
