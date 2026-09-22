# Provision

Provision helps application teams without dedicated platform teams describe an application once and deploy it safely across local hosts and selected cloud targets.

## Language

**Application**:
A cohesive deployment and promotion boundary composed of related components, independent of any particular repository, environment, or execution target. Its components may be built from more than one source repository.
_Avoid_: Project, stack

**Application Team**:
The team that builds and deploys an application and is the primary user of Provision.
_Avoid_: Platform team, operator

**Environment**:
A named deployment of an application with its own behavior, policy, implementation choices, secrets, domains, sizing, lifecycle, and one active revision outside a deployment transition.
_Avoid_: Stage, namespace

**Persistent Environment**:
An environment intended to remain until explicitly destroyed, independent of conventional names such as development or production.
_Avoid_: Production environment

**Ephemeral Environment**:
An environment with an explicit expiry or destruction condition.
_Avoid_: Preview environment

**Component**:
A logical application role drawn from Provision's fixed vocabulary, such as HTTP service, database, cache, object store, realtime server, queue, worker, task, or schedule.
_Avoid_: Resource, workload

**Implementation**:
The single authoritative environment-specific way to realize a component while preserving its declared intent.
_Avoid_: Provider, driver, renderer

**Queue**:
A first-class component that carries asynchronous messages from producers to workers with declared delivery semantics.
_Avoid_: Worker queue, channel

**Object Store**:
A first-class component that stores application-addressable objects independently of the machines or processes using them.
_Avoid_: Volume, filesystem

**Worker**:
A persistent component that consumes work from a queue.
_Avoid_: Job, task

**Task**:
A run-to-completion component that succeeds or fails when invoked manually, by a schedule, or by another component.
_Avoid_: One-shot worker, job

**Schedule**:
A first-class component that defines when a task is invoked and its overlap, retry, and failure policy. A scheduler is an environment-specific implementation of schedules rather than an application component.
_Avoid_: Scheduler, cron job, scheduled worker

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

**Secret Reference**:
An identifier that tells an implementation where to obtain a secret without placing the secret value in committed configuration, plans, or history.
_Avoid_: Secret value, encrypted configuration value

**Resolved Configuration**:
The complete, inspectable configuration produced from explicitly ordered documents and intentional overrides before validation or execution.
_Avoid_: Effective files, discovered configuration

**Environment Configuration Revision**:
An immutable version of an environment's resolved behavior values, feature flags, implementation selections, and policy references, recorded independently of the application revision.
_Avoid_: Environment version, mutable settings

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

**Promotion**:
An optional deployment workflow that selects an exact revision already deployed or verified in one environment for deployment to another. Environments may also receive revisions independently and do not form an implicit pipeline.
_Avoid_: Stage progression, rebuild

**Verification Evidence**:
An immutable record of checks performed against a revision in an environment. It may accompany a promotion as provenance but does not replace verification required by the destination environment.
_Avoid_: Global approval, trusted build

**Data Refresh**:
A named operation that populates an environment from another data source through application-defined extraction, anonymization, and loading actions. Raw production data must be anonymized before crossing into a non-production environment.
_Avoid_: Database copy, production clone

**Data Snapshot**:
An immutable record identifying the output of a successful data refresh by source reference, creation time, anonymization action version, compatibility identifier, digest, and retention status, without containing sensitive source data.
_Avoid_: Database dump, backup

**Environment Lease**:
An optional, expiring declaration that an application team member is using a shared environment and why. It prevents accidental replacement but can be explicitly overridden with an audit record.
_Avoid_: Deployment lock, ownership

**Active Revision**:
The one revision currently selected for an environment's application traffic and work. A deployment may temporarily prepare a candidate revision, but does not create multiple active revisions within that environment.
_Avoid_: Latest build, deployed components

**Blue-Green Deployment**:
A deployment that keeps the existing revision available while a candidate is prepared and verified, followed by a reversible handoff where the selected implementation supports it.
_Avoid_: Zero-downtime deployment

**Audit Record**:
An attributable, immutable account of a product operation, its referenced inputs, approvals, outcome, and failure details, without resolved secret values or sensitive dataset contents.
_Avoid_: Log line, mutable history
