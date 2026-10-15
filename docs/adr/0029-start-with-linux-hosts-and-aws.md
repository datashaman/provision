# Start with Linux hosts and AWS

**Status: Target roadmap retained; initial-release sequencing amended by [ADR 0080](0080-scope-first-release-to-mediated-artifact-staging.md).** AWS and full local/remote deployment coverage are deferred from the first useful release. The original decision follows.

The first supported target set is the current or a remote Linux host plus AWS services. Portable intent stays independent of AWS terminology, but Provision does not claim multi-cloud parity before equivalent implementations and capability validation exist. This concentrates delivery on the concrete local, EC2, ECS, Lambda, and AWS-managed-service scenarios while preserving a provider-neutral application model.
