# Open questions

Nothing in this document is decided. These questions should be resolved before selecting a technical architecture.

## Component model

- What portable scaling semantics are useful without becoming misleading?

## Deployment semantics

- How are WebSocket connections drained or reconnected?
- How are worker jobs made idempotent and in-flight work observed?
- How are duplicate scheduled executions prevented?
- What migration guarantees are expected for databases and durable state?

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
