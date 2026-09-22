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

**Persistent Environment**:
An environment intended to remain until explicitly destroyed, independent of conventional names such as development or production.
_Avoid_: Production environment

**Ephemeral Environment**:
An environment with an explicit expiry or destruction condition.
_Avoid_: Preview environment

**Component**:
A logical application role drawn from Provision's fixed vocabulary, such as HTTP service, database, cache, realtime server, queue, worker, task, or scheduler.
_Avoid_: Resource, workload

**Implementation**:
The single authoritative environment-specific way to realize a component while preserving its declared intent.
_Avoid_: Provider, driver, renderer

**Queue**:
A first-class component that carries asynchronous messages from producers to workers with declared delivery semantics.
_Avoid_: Worker queue, channel

**Worker**:
A persistent component that consumes work from a queue.
_Avoid_: Job, task

**Task**:
A run-to-completion component that succeeds or fails when invoked manually, by a scheduler, or by another component.
_Avoid_: One-shot worker, job

**Scheduler**:
A component that invokes tasks according to recurring or one-time schedules.
_Avoid_: Cron job, scheduled worker

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

**Configuration Fragment**:
An optional reusable group of implementation choices that an environment may include without creating another domain entity.
_Avoid_: Deployment profile, environment class

**Artifact**:
An immutable deployable output supplied to Provision or produced by a build.
_Avoid_: Asset, binary

**Build**:
Application-defined work that converts source inputs into one or more artifacts for a revision.
_Avoid_: Deployment, release

**Action**:
Application-defined executable work referenced by declarative configuration with an explicit phase and execution contract.
_Avoid_: Configuration code, hook

**Revision**:
An immutable manifest that identifies the complete set of component artifacts for an application version.
_Avoid_: Release, artifact

**Retention Policy**:
An explicit rule that determines whether a managed stateful component is retained or deleted when its environment is destroyed; retention is the default.
_Avoid_: Delete flag

**Adoption**:
An explicit operation that transfers an external component into Provision's managed lifecycle after identity and consequences are verified.
_Avoid_: Import, automatic discovery

**Deployment**:
An attempt to place a revision into an environment according to that environment's selected implementations and policy.
_Avoid_: Reconciliation, rollout

**Blue-Green Deployment**:
A deployment that keeps the existing revision available while a candidate is prepared and verified, followed by a reversible handoff where the selected implementation supports it.
_Avoid_: Zero-downtime deployment
