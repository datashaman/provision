# Provision

Provision helps application teams without dedicated platform teams describe an application once and deploy it safely across local hosts and selected cloud targets.

## Language

**Application**:
A logical deployable system composed of related components, independent of any particular environment or execution target.
_Avoid_: Project, stack

**Application Team**:
The team that builds and deploys an application and is the primary user of Provision.
_Avoid_: Platform team, operator

**Environment**:
A named deployment of an application with its own behavior, policy, implementation choices, secrets, domains, sizing, and lifecycle.
_Avoid_: Stage, namespace

**Component**:
A logical application role drawn from Provision's fixed vocabulary, such as HTTP service, database, cache, realtime server, worker, scheduler, or queue.
_Avoid_: Resource, workload

**Implementation**:
An environment-specific way to realize a component while preserving its declared intent.
_Avoid_: Provider, driver, renderer

**Execution Target**:
The place where executable components run, such as the current machine, a remote host, EC2, ECS, or Lambda.
_Avoid_: Cluster, platform

**Managed Component**:
A component whose underlying service lifecycle Provision is permitted to create, update, and delete within documented limits.
_Avoid_: Owned resource

**External Component**:
A component whose underlying service exists outside Provision's lifecycle and may only be validated and bound to the application.
_Avoid_: Unmanaged component

**Portable Intent**:
The stable meaning and requirements of a component across environments, despite explicit differences between implementations.
_Avoid_: Identical behavior, provider neutrality

**Revision**:
An immutable built version of an application intended for deployment or promotion between environments.
_Avoid_: Release, build

**Deployment**:
An attempt to place a revision into an environment according to that environment's selected implementations and policy.
_Avoid_: Reconciliation, rollout

**Blue-Green Deployment**:
A deployment that keeps the existing revision available while a candidate is prepared and verified, followed by a reversible handoff where the selected implementation supports it.
_Avoid_: Zero-downtime deployment

