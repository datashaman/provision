# Open questions

Nothing in this document is decided. These questions should be resolved before selecting a technical architecture.

## Deployment semantics

- What migration guarantees are expected for databases and durable state?
- How are writes synchronized during a store cutover, and what data-loss guarantee applies to rollback after new writes reach the candidate generation?
- Which store types may rebuild rather than copy data, and how is that declared safely?
- How does required store blue-green work for an external component that Provision cannot create or mutate?

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
