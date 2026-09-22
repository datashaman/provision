# Open questions

Nothing in this document is decided. These questions should be resolved before selecting a technical architecture.

## Configuration experience

- One file, multiple files, or a directory convention?
- How are application defaults and environment overrides merged?
- How are invalid or contradictory overrides reported?
- How are reusable configuration fragments included and overridden?
- How should secrets be referenced without leaking into plans or history?

## Component model

- Is the initial component vocabulary complete?
- What portable scaling semantics are useful without becoming misleading?

## Deployment semantics

- What exact guarantees does “blue-green” promise for each component type?
- When should deployment fail rather than fall back to an in-place update?
- How are WebSocket connections drained or reconnected?
- How are worker jobs made idempotent and in-flight work observed?
- How are duplicate scheduled executions prevented?
- What migration guarantees are expected for databases and durable state?

## Environment lifecycle

- How are preview environments created, expired, and destroyed?
- What does promotion preserve between environments?
- Which values are behavior configuration versus infrastructure configuration?
- What audit history and approval flow are required?

## Technical architecture—not yet discussed

- CLI, local service, hosted service, daemon, library, or hybrid?
- Push-based deployment, pull-based deployment, or reconciliation?
- Programming language and module boundaries?
- State and locking model?
- Provisioning implementation: SDKs, Terraform/OpenTofu, CloudFormation, Pulumi, or another mechanism?
- Plugin model, if any?
- Credential and secret handling?
- Failure recovery and resumability?
- Concurrency and multi-user behavior?
- Testing, simulation, and local development strategy?
