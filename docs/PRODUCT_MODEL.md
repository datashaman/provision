# Product model

## Problem statement

Application teams without dedicated platform teams need a consistent way to describe a web application and deploy it into materially different environments without maintaining unrelated deployment definitions for each target.

The product should preserve the application's intent while allowing each environment to choose appropriate implementations.

## Product promise

Provision's core job is to let an application team describe an application once and safely deploy it across local hosts and selected cloud targets.

The intended scope runs from development through small and medium production deployments. Production safety must be genuine, but the initial product does not promise the governance, disaster recovery, organizational policy, or operational support expected of a mission-critical enterprise platform.

Provisioning supporting services is in scope when necessary to deploy the application. Provision is not intended to become a general infrastructure-management product.

Provision supports both application-defined builds and artifacts built elsewhere. Deployment always consumes an immutable revision manifest so the same revision can be promoted between environments without rebuilding.

## Core concepts

### Application

An application describes the logical system being deployed. It is a cohesive deployment and promotion boundary, not a source-repository boundary: its components may be built from multiple repositories but are recorded together as one revision. It owns component relationships and portable behavioral requirements, not provider-specific infrastructure.

### Environment

An environment is a named instance of an application, such as local development, a shared development host, preview, staging, or production.

Environment is first-class because application behavior may differ between environments. An environment may define:

- behavior values and feature flags;
- domains and public endpoints;
- secret references;
- component sizing and availability requirements;
- deployment safety policy;
- persistent or ephemeral lifecycle;
- an implementation selection for each component.

Provision supports two environment workflows. A revision may be deployed independently to any environment, or the exact same revision may be promoted from one environment to another without rebuilding it. Environment names do not create an implicit promotion pipeline.

A shared testing environment may receive whichever revision a team needs to test and may use anonymized data derived from production. That use does not make it a mandatory pre-production stage. A team may separately choose a pipeline-style workflow in which a revision is promoted through environments.

Promotion preserves the source verification evidence as provenance. Destination policy decides which checks and approvals must run again; evidence from another environment cannot silently authorize activation.

Environment names such as development, preview, staging, and production have no hidden behavior. An environment explicitly declares a persistent or ephemeral lifecycle, and its approval, retention, destruction protection, sizing, and deployment policy remain visible.

### Component

A component represents an application role. The initial vocabulary is deliberately small and fixed:

- HTTP service;
- database;
- cache;
- object store;
- realtime server;
- queue;
- worker;
- task;
- schedule;

This list is a product vocabulary, not an extensible resource-definition system.

A queue is independent of its producers and consumers. An object store contains application-addressable objects and is distinct from an implementation's attached filesystem or volume. A worker is a persistent queue consumer. A task runs to completion when invoked. A schedule identifies a target task and declares timing, overlap, retry, and failure policy. Cron, systemd timers, application cron runners, and cloud schedulers are possible implementations of that intent; “scheduler” is not a separate application component.

### Implementation choice

The same logical component may have different implementations in different environments.

Examples:

- PostgreSQL managed locally or provided by RDS;
- Redis managed locally or provided by ElastiCache;
- an object store using local or S3-compatible storage, or an external managed service;
- a realtime server run by systemd, ECS, or an AWS WebSocket service;
- a worker run as a systemd process, an ECS service consuming SQS, a per-job container, or a Lambda function;
- a schedule emitted by cron, a systemd timer, an application scheduler, or EventBridge Scheduler.

The product must validate whether a selected implementation can satisfy the component's declared requirements.

Executable components express portable capacity intent as either fixed capacity or minimum and maximum capacity with a concurrency target. An implementation interprets capacity according to its execution model: a host or container implementation may use process or replica counts, while Lambda-style execution uses concurrency bounds. Provider-specific metrics, triggers, and scaling algorithms remain visible in namespaced options.

The portability promise is **portable intent**, not identical behavior. Meaningful differences in scaling, availability, rollout safety, operational limits, and cost must remain visible. An environment that cannot satisfy a declared requirement must fail validation rather than silently weaken the requirement.

Portable component fields form the common model. Provider- or implementation-specific configuration remains available through explicit namespaced options rather than being hidden or silently inferred.

Reusable implementation selections may be packaged as optional configuration fragments. They are not a separate domain entity, and the resolved environment remains authoritative.

Each component has exactly one authoritative implementation in an environment. Blue-green deployment uses two revisions of that implementation rather than two unrelated implementations. A controlled migration may temporarily use source and destination implementations but must finish with one authoritative binding.

Likewise, a database, cache, or object-store component remains one logical component while a store transition temporarily creates active and candidate physical generations. The transition finishes with one authoritative generation; the candidate is not modeled as a second application component.

### Component ownership

Every stateful or supporting component is either managed or external.

- A managed component may be created, updated, and deleted by Provision within documented limits.
- An external component exists outside Provision's lifecycle. Provision may validate it and bind the application to it but must not mutate or delete it.

Ownership is explicit per component so one environment can mix managed and external services safely.

Managed scope is limited to application-scoped resources: workloads, application databases, caches, object stores, queues, ingress, certificates, application DNS records, and application-level backup policy. Foundational infrastructure—including cloud accounts, network strategy, DNS zones, organizational identity, and secret stores—is initially external.

Every managed stateful component has an explicit retention policy, with retention as the safe default. Ephemeral environments may explicitly select automatic deletion. An external component can become managed only through an explicit adoption operation that verifies its identity, records provenance, and presents the lifecycle responsibility being assumed.

### Builds, artifacts, and revisions

A build is application-defined work that produces immutable artifacts. Provision may run the build or accept artifacts from another build system.

A revision is an immutable manifest identifying the complete set of component artifacts for one application version. Unchanged components may reuse existing artifacts, but environments deploy and promote the complete revision rather than an unrecorded mixture of component versions.

Creating a new artifact creates a new complete revision. During deployment, unchanged artifacts and components may be skipped safely, and the same revision may be reapplied, but the environment's recorded desired version remains a complete revision rather than a partial deployment.

Environment-specific behavior and operational choices are versioned independently as an immutable environment configuration revision. A deployment records both the application revision and the resolved environment configuration revision, so changing a feature flag, implementation selection, or policy is attributable without pretending the application artifact changed.

### Declarative configuration and custom actions

Configuration remains declarative, but it may reference application-defined executable actions for builds, migrations, verification, and lifecycle work. Custom code is not executed merely by loading configuration.

An action declares its phase and execution contract, including inputs, outputs, timing, retry, and failure behavior. It also declares whether it is idempotent, resumable from an explicit checkpoint, or restricted to a single attempt. Provision records every attempt and never claims exactly-once execution of arbitrary application code. The eventual execution mechanism—such as local command, container, remote command, or function—belongs to later technical architecture work.

Configuration is organized as explicitly referenced logical documents. A project may keep them in one physical file or many files, but meaning does not depend on filenames or automatic directory scanning.

Documents are evaluated in explicit include order. Maps merge by key and lists replace rather than concatenate. Repeating a scalar value is an error unless the later document marks it as an intentional override. Before execution, Provision exposes the fully resolved configuration together with each value's source.

Validation is atomic with respect to planning: Provision reports every detectable contradiction with its logical path and source document, then rejects the whole plan before making changes. It must not apply a valid-looking subset of an invalid environment.

Committed configuration contains secret references, never production secret values. Local environments may reference process environment variables, ignored local files, or a local secret provider. Plans, deployment history, and diagnostics must not reveal resolved values.

### Environment lifecycle

An ephemeral environment has an explicit expiry or destruction condition. Expiry initiates automatic destruction, may be renewed explicitly, and still honors each stateful component's retention policy. Failed cleanup remains visible and retryable rather than being treated as successful destruction.

External authorization policy decides who may create or renew an ephemeral environment. Provision enforces its declared maximum lifetime and resource limits. Creation and renewal are explicit and audited; activity alone never extends expiry.

An environment has one active application revision. A deployment may temporarily run the current and candidate revisions for blue-green handoff, but after the transition only one is active. Deployments to the same environment are serialized. Concurrent isolated testing uses separate environments rather than several active revisions hidden inside one shared environment.

A shared environment may have an optional expiring lease that identifies who is using it and why. Replacing a deployment protected by another user's lease requires explicit acknowledgement and leaves an audit record. The lease communicates coordination intent; it does not create permanent ownership.

### Production-derived test data

Refreshing a shared testing environment from production-derived data is a named product workflow. Provision coordinates and records the operation, while extraction, anonymization, transformation, and loading are application-defined actions because their correctness depends on the application's schema and policies.

Raw production data must be anonymized within the production security boundary before it crosses into a non-production environment. If that boundary cannot be demonstrated, the refresh fails closed. Data-refresh history remains distinct from application revision history because either may change independently.

Each successful refresh produces a data snapshot record containing source reference, creation time, anonymization-action version, compatibility identifier, digest, and retention status without storing sensitive contents. Compatibility with an application revision must be checked explicitly rather than inferred from recency.

Refreshes prefer staged loading followed by a switch. If an implementation cannot provide that behavior, the operation requires either a recoverable backup or explicit approval of a destructive mode. A partial load is a failed refresh and must never be presented as the active successful snapshot.

## Required deployment scenarios

At minimum, the product model must be able to express:

1. The current machine using systemd with a local database and cache.
2. Another machine on the local network or VPN, accessed remotely, using systemd with local data services.
3. An EC2 instance using systemd with host-local database and cache services.
4. An EC2 instance using systemd with AWS-managed database and cache services.
5. ECS or Fargate services with AWS-managed supporting services.
6. Lambda for workloads compatible with event-driven serverless execution.
7. Hybrid environments where different components use different execution options.

Lambda may implement HTTP handlers, event-driven tasks, and queue consumers when their declared duration, concurrency, payload, and state requirements fit its capabilities. A schedule may invoke a Lambda-backed task. A persistent worker or realtime server does not map directly to Lambda, although a managed event or WebSocket frontend may invoke Lambda handlers. Incompatible selections fail validation.

## Blue-green expectation

Blue-green is a product requirement where the selected implementation can provide it safely. The meaning differs by component role:

- HTTP services prepare the candidate at a separate endpoint, pass required gates, switch new traffic through a stable entry point, drain old requests, and retain the previous revision for the declared rollback window. Required mode fails when stable routing and reversible handoff are unavailable.
- Realtime services route new connections to the candidate while the old revision drains existing connections until a configured deadline. Remaining clients receive a reconnect signal and are disconnected. Provision does not claim live socket migration or uninterrupted connections.
- Workers stop the old revision from claiming new work, expose and drain in-flight work, then activate the candidate consumer. Required mode needs acknowledgement or visibility semantics, and application-level idempotency remains necessary. The guarantee is at-least-once processing, not exactly-once execution.
- Schedules have one active emitting revision and use a fenced handoff from old to new. Tasks must tolerate duplicate invocation around failures at the handoff boundary; Provision does not claim universal exactly-once scheduling.
- Databases, caches, and object stores may prepare a separately addressable candidate generation, synchronize or rebuild it as appropriate, verify it, switch authority, and retain the previous generation for a declared rollback window. This supports engine upgrades, upsizing, storage-class changes, and implementation migrations without treating the store as permanently shared or replacing it in place.

A store transition is driven by an environment configuration revision and may occur while the application revision remains unchanged. Schema migration is a separate application-defined change to the data contract; a deployment may perform a store transition, a schema migration, or both.

Each component selects an explicit rollout requirement:

- **required** fails validation when blue-green behavior is unavailable;
- **preferred** uses blue-green when available and requires explicit approval before an in-place fallback;
- **replace** permits in-place replacement.

There is no silent fallback. Before activation, every verification action required by destination policy must pass and any required manual approval must be recorded. Failed post-activation checks trigger automatic rollback when the implementation supports it; otherwise the failure and required recovery action remain explicit.

For a managed store in required mode, the selected implementation must provide an isolated candidate generation, synchronization or rebuilding appropriate to the store, verification, authority cutover, and retention of the previous generation for the declared rollback window. Provider-managed in-place or rolling changes satisfy required mode only when they expose equivalent isolation, verification, cutover, and rollback guarantees; otherwise they are preferred or replace behavior.

Required store blue-green guarantees that no acknowledged write is lost during the forward cutover. The implementation may continuously replicate or use a declared, bounded write pause for final synchronization. The plan exposes the maximum expected write interruption, and validation fails when the environment's requirement is stricter than the implementation can satisfy.

Rollback after the candidate accepts writes is a separate guarantee because the previous generation immediately becomes stale. An environment selects **zero-loss** rollback, **bounded-loss** rollback with an explicit maximum, or **forward-only** recovery. Zero-loss requires reverse synchronization or an equivalent mechanism; retaining the previous generation alone does not qualify. Blue-green therefore always guarantees a lossless forward cutover but does not disguise a weaker rollback guarantee.

Each store declares whether its data is **authoritative**, **derived**, or **ephemeral**. Authoritative data must be synchronized. Derived data may be rebuilt from a declared authoritative source, and ephemeral data may be discarded. Databases and object stores default to authoritative; caches default to derived, with an explicit override when a cache contains authoritative state. Rebuilding and verification can satisfy blue-green for a derived or ephemeral candidate without copying the old generation.

Schema migration Actions declare the application revisions compatible with the resulting data contract. The default pattern is expand-and-contract: expansion happens before candidate activation, both application revisions remain compatible throughout the rollback window, and destructive contraction waits until neither the old application revision nor old store generation remains a rollback target.

An external store can satisfy required blue-green only when its owner supplies verifiable candidate addressing, synchronization or rebuilding, health evidence, cutover, and rollback semantics. Provision may coordinate declared bindings and Actions within its granted authority but does not gain lifecycle control over the external component. Missing capabilities fail validation.

After the rollback window, a transition cleanup policy governs the previous generation separately from the surviving component's retention policy. Cleanup requires successful verification, expiry of the rollback window, and an auditable decision. The safe default replaces the running previous generation with a verified retained backup or snapshot rather than silently destroying all recovery material.

A multi-component deployment is not transactional. Provision orders work, records explicit progress points, and resumes safely where possible. Stateless routing and workloads may be rolled back, while stateful changes may require declared compensating or forward-recovery actions. Partial execution remains visible and is never reported as an atomic success or disappearance.

## Audit history

Provision records resolved configuration, plans, approvals, deployments, promotions, verification results, custom actions, data refreshes, adoption, destruction, expiry, rollback, lease overrides, and safety overrides. Each record identifies the actor, time, referenced inputs, outcome, and failure details without disclosing resolved secrets or sensitive dataset contents.

## Environment-aware behavior

Applications should receive explicit environment configuration and feature flags. Environment-specific business behavior should not depend on detecting an infrastructure provider or execution implementation.

Useful runtime metadata may include the environment name, lifecycle, application revision, and active deployment color. The exact delivery mechanism is not yet decided.

## Product constraints established so far

- Component implementation is selectable per environment and per component.
- A remote development host is distinct from the machine running the deployment command.
- Persistent services are first-class, not incidental attachments to an HTTP service.
- Workers and schedules are first-class and may use different execution implementations.
- Queues are first-class; workers consume them rather than own them implicitly.
- Workers are persistent consumers, while tasks run to completion and may be triggered by schedules.
- Each component has one authoritative implementation per environment.
- The product must describe scaling intent without assuming one provider's vocabulary.
- Unsupported combinations must be identified before an unsafe deployment begins.
- Provider-specific options must be explicit and namespaced.
- Environment names must not imply hidden behavior or safety policy.
- Reusable configuration fragments must not displace the environment as the authoritative resolved configuration.
- Stateful retention defaults to safe preservation, and adoption into managed lifecycle is always explicit.
- Applications may span repositories but form one revision boundary.
- Environments may receive revisions independently; promotion is an optional exact-revision workflow.
- Configuration documents are included explicitly and do not gain meaning from filenames or directory scanning.
- Committed configuration contains references to secrets, not secret values.
- Verification evidence accompanies promotion as provenance, while destination policy remains authoritative.
- Shared environments have one active revision and serialize deployments.
- Production-derived data is anonymized before leaving the production boundary and enters non-production only through an explicit data refresh.
- Invalid resolved configuration is rejected in full before changes begin.
- Object stores are first-class components; implementation-local volumes are not.
- Each deployment identifies both an application revision and an environment configuration revision.
- Blue-green requirements and fallback behavior are explicit per component, with no silent degradation.
- Candidate activation is gated by destination verification and approval policy.
- Ephemeral creation and renewal are explicit, bounded, authorized, and audited.
- Shared environments may use expiring coordination leases without creating permanent ownership.
- Data snapshots are identifiable independently of application revisions, and partial refreshes never become successful snapshots.
- Product operations produce attributable audit records without exposing secrets or sensitive data.
- Portable executable scaling supports fixed capacity or bounded capacity with concurrency intent; provider algorithms remain explicit.
- HTTP, realtime, worker, and schedule handoffs have role-specific blue-green guarantees rather than one generic promise.
- Multi-component deployments are ordered and resumable, not transactional.
- Actions declare retry safety, and arbitrary custom code is never presented as exactly-once.
- Lambda is eligible only for component roles whose declared behavior fits event-driven serverless execution.
- Store blue-green operates on physical generations of one logical component and applies to upgrades, upsizing, storage changes, and implementation migration.
- Store transitions are environment changes distinct from application-defined schema migrations.
- Required store blue-green loses no acknowledged writes during forward cutover; rollback loss is declared separately.
- Store data is authoritative, derived, or ephemeral, determining whether a candidate must synchronize, rebuild, or may start empty.
- Schema migration compatibility spans the application and store rollback window before destructive contraction.
- External stores must expose verifiable transition capabilities to satisfy required blue-green.
- Previous store generations follow an explicit transition cleanup policy after the rollback window.

## Not yet decided

No implementation or technical architecture has been selected. In particular, the repository does not yet decide whether the product is a CLI, service, library, daemon, or combination of these; how it provisions resources; how configuration is represented; or how deployment state is stored.
