# Open questions

Nothing in this document is decided. These questions should be resolved before selecting a technical architecture.

## Product scope

- Is the primary user an individual developer, an application team, or a platform team?
- Does the product build artifacts, deploy prebuilt artifacts, or support both?
- Does it own DNS, certificates, secrets, networking, and backups, or only reference existing facilities?
- Is it intended for production operations or primarily for consistent development and small deployments?
- How much provider-specific configuration should remain visible?
- Should deployment profiles be explicit user-facing objects or merely reusable configuration fragments?

## Configuration experience

- One file, multiple files, or a directory convention?
- How are application defaults and environment overrides merged?
- How are invalid or contradictory overrides reported?
- What is the escape hatch for provider-specific requirements?
- Should configuration be declarative only, or may users include code?
- How should secrets be referenced without leaking into plans or history?

## Component model

- Is the initial component vocabulary complete?
- Are queues first-class components or bindings used by workers?
- Is a run-to-completion task a worker mode or a separate component type?
- Can one logical component have multiple implementations in the same environment?
- What portable scaling semantics are useful without becoming misleading?

## Deployment semantics

- What exact guarantees does “blue-green” promise for each component type?
- When should deployment fail rather than fall back to an in-place update?
- How are WebSocket connections drained or reconnected?
- How are worker jobs made idempotent and in-flight work observed?
- How are duplicate scheduled executions prevented?
- What migration guarantees are expected for databases and durable state?

## Environment lifecycle

- Which environment categories are built in, if any?
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
