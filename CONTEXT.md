# Provision

Provision helps application teams without dedicated platform teams describe an application once and deploy it safely across local hosts and selected cloud targets.

## Language

**Application**:
A cohesive deployment and promotion boundary composed of related components, independent of any particular repository, environment, or execution target. Its components may be built from more than one source repository.
_Avoid_: Project, stack

**Application Team**:
The team that builds and deploys an application and is the primary user of Provision.
_Avoid_: Platform team, operator

**Logical Identity**:
A stable identity for an application, environment, component, or other durable domain object that remains unchanged when its display name changes.
_Avoid_: Display name, provider resource name

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

**Component Relationship**:
A logical reference from one component to another, classified as requires when activation depends on availability or uses when it is only a runtime association.
_Avoid_: Hostname, startup script

**Binding**:
The environment-specific connection information and secret references produced from a component relationship without exposing infrastructure details in the application model.
_Avoid_: Hard-coded endpoint, connection string value

**Endpoint**:
An exposure attached to an HTTP or realtime component that declares visibility, protocol, domain, TLS, routing, and traffic-handoff requirements.
_Avoid_: Ingress component, load balancer

**Health Contract**:
A component's declared liveness, readiness, and candidate-verification semantics, independent of how its implementation performs the checks.
_Avoid_: Health URL, provider check

**Implementation**:
The single authoritative environment-specific way to realize a component while preserving its declared intent.
_Avoid_: Provider, driver, renderer

**Store Generation**:
A separately addressable physical realization of one logical database, cache, or object-store component. A store transition may temporarily maintain active and candidate generations, but finishes with one authoritative generation.
_Avoid_: Second database component, store revision

**Store Transition**:
The verified replacement of a store's active generation with a synchronized candidate generation, commonly for an engine upgrade, capacity change, storage-class change, or implementation migration.
_Avoid_: Schema migration, in-place resize

**Store Data Role**:
Whether a store's contents are authoritative and must be synchronized, derived and may be rebuilt from an authoritative source, or ephemeral and may be discarded.
_Avoid_: Durability setting, backup class

**Store Rollback Guarantee**:
The declared data-safety outcome if authority returns to a previous store generation after cutover: zero-loss, bounded-loss, or forward-only with no safe return to the old generation.
_Avoid_: Retention window, backup policy

**Transition Cleanup Policy**:
The rule for retaining, snapshotting, or destroying a previous store generation after its rollback window and verification requirements have completed.
_Avoid_: Component retention policy, automatic deletion

**Schema Migration**:
Application-defined work that changes the data contract understood by application revisions, independently of whether the underlying store generation changes.
_Avoid_: Store upgrade, database replacement

**Queue**:
A first-class component that carries asynchronous messages from producers to workers with declared delivery, ordering, retention, acknowledgement, retry, dead-letter, and deduplication semantics.
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

**Task Invocation**:
An immutable record of one Task execution, including its application and environment configuration revisions, trigger, referenced inputs, attempts, result, timing, and produced artifacts.
_Avoid_: Worker, process

**Schedule**:
A first-class component that defines when a task is invoked, including timezone, daylight-saving, overlap, missed-run, retry, failure, and bounded catch-up policy. A scheduler is an environment-specific implementation of schedules rather than an application component.
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

**Capacity Intent**:
A component's portable fixed capacity or bounded scaling and concurrency requirements, interpreted according to its selected implementation without hiding implementation-specific scaling behavior.
_Avoid_: Instance count, autoscaling algorithm

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

**Secret Rotation**:
An explicit, auditable environment event in which a secret reference resolves to a new value and each consumer follows its declared dynamic-read, reload, or rollout behavior.
_Avoid_: Configuration edit, secret value change

**Artifact**:
An immutable deployable output supplied to Provision or produced by a build.
_Avoid_: Asset, binary

**Artifact Attestation**:
Referenced evidence about an artifact's provenance, signature, security assessment, or other policy-relevant property, validated by Provision but produced elsewhere.
_Avoid_: Artifact, scanner result embedded in configuration

**Build**:
Application-defined work that converts source inputs into one or more artifacts for a revision.
_Avoid_: Deployment, release

**Action**:
Application-defined executable work referenced by declarative configuration with an explicit phase, execution contract, and retry classification. An action may be idempotent, checkpoint-resumable, or explicitly single-attempt.
_Avoid_: Configuration code, hook

**Revision**:
An immutable manifest that identifies the complete set of component artifacts for an application version.
_Avoid_: Release, artifact

**Retention Policy**:
An explicit rule that determines whether a managed stateful component is retained or deleted when its environment is destroyed; retention is the default.
_Avoid_: Delete flag

**Recovery Policy**:
The required backup frequency, retention, acceptable data loss, acceptable recovery time, and restore-verification behavior for an authoritative managed store.
_Avoid_: Retention policy, provider backup switch

**Restore**:
An operation that materializes retained data into a candidate store generation or recovery environment for verification before any explicit authority cutover.
_Avoid_: In-place overwrite, rollback

**Adoption**:
An explicit operation that transfers an external component into Provision's managed lifecycle after identity and consequences are verified.
_Avoid_: Import, automatic discovery

**Deployment**:
An ordered, resumable attempt to place an application revision and environment configuration revision into an environment according to its selected implementations and policy. It records partial progress and recovery rather than claiming transactionality across components.
_Avoid_: Reconciliation, rollout

**Plan**:
An immutable proposed operation bound to exact application and environment configuration revisions, observed state, capabilities, and artifact digests. A relevant change makes it stale and requires replanning.
_Avoid_: Preview text, reusable script

**Drift**:
A difference between an environment's recorded state and its observed state that must be reported with ownership and safety context before any authorized reconciliation.
_Avoid_: Automatic repair, configuration change

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
A deployment that keeps an active application revision or store generation available while a candidate is prepared, synchronized when necessary, and verified, followed by a reversible handoff where the selected implementation supports it.
_Avoid_: Zero-downtime deployment

**Audit Record**:
An attributable, immutable account of a product operation, its referenced inputs, approvals, outcome, and failure details, without resolved secret values or sensitive dataset contents.
_Avoid_: Log line, mutable history
