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

Environment names such as development, preview, staging, and production have no hidden behavior. An environment explicitly declares a persistent or ephemeral lifecycle, and its approval, retention, destruction protection, sizing, and deployment policy remain visible.

### Component

A component represents an application role. The initial vocabulary is deliberately small and fixed:

- HTTP service;
- database;
- cache;
- realtime server;
- queue;
- worker;
- task;
- schedule;

This list is a product vocabulary, not an extensible resource-definition system.

A queue is independent of its producers and consumers. A worker is a persistent queue consumer. A task runs to completion when invoked. A schedule identifies a target task and declares timing, overlap, retry, and failure policy. Cron, systemd timers, application cron runners, and cloud schedulers are possible implementations of that intent; “scheduler” is not a separate application component.

### Implementation choice

The same logical component may have different implementations in different environments.

Examples:

- PostgreSQL managed locally or provided by RDS;
- Redis managed locally or provided by ElastiCache;
- a realtime server run by systemd, ECS, or an AWS WebSocket service;
- a worker run as a systemd process, an ECS service consuming SQS, a per-job container, or a Lambda function;
- a schedule emitted by cron, a systemd timer, an application scheduler, or EventBridge Scheduler.

The product must validate whether a selected implementation can satisfy the component's declared requirements.

The portability promise is **portable intent**, not identical behavior. Meaningful differences in scaling, availability, rollout safety, operational limits, and cost must remain visible. An environment that cannot satisfy a declared requirement must fail validation rather than silently weaken the requirement.

Portable component fields form the common model. Provider- or implementation-specific configuration remains available through explicit namespaced options rather than being hidden or silently inferred.

Reusable implementation selections may be packaged as optional configuration fragments. They are not a separate domain entity, and the resolved environment remains authoritative.

Each component has exactly one authoritative implementation in an environment. Blue-green deployment uses two revisions of that implementation rather than two unrelated implementations. A controlled migration may temporarily use source and destination implementations but must finish with one authoritative binding.

### Component ownership

Every stateful or supporting component is either managed or external.

- A managed component may be created, updated, and deleted by Provision within documented limits.
- An external component exists outside Provision's lifecycle. Provision may validate it and bind the application to it but must not mutate or delete it.

Ownership is explicit per component so one environment can mix managed and external services safely.

Managed scope is limited to application-scoped resources: workloads, application databases, caches, queues, ingress, certificates, application DNS records, and application-level backup policy. Foundational infrastructure—including cloud accounts, network strategy, DNS zones, organizational identity, and secret stores—is initially external.

Every managed stateful component has an explicit retention policy, with retention as the safe default. Ephemeral environments may explicitly select automatic deletion. An external component can become managed only through an explicit adoption operation that verifies its identity, records provenance, and presents the lifecycle responsibility being assumed.

### Builds, artifacts, and revisions

A build is application-defined work that produces immutable artifacts. Provision may run the build or accept artifacts from another build system.

A revision is an immutable manifest identifying the complete set of component artifacts for one application version. Unchanged components may reuse existing artifacts, but environments deploy and promote the complete revision rather than an unrecorded mixture of component versions.

Creating a new artifact creates a new complete revision. During deployment, unchanged artifacts and components may be skipped safely, and the same revision may be reapplied, but the environment's recorded desired version remains a complete revision rather than a partial deployment.

### Declarative configuration and custom actions

Configuration remains declarative, but it may reference application-defined executable actions for builds, migrations, verification, and lifecycle work. Custom code is not executed merely by loading configuration.

An action declares its phase and execution contract, including inputs, outputs, timing, retry, and failure behavior. The eventual execution mechanism—such as local command, container, remote command, or function—belongs to later technical architecture work.

Configuration is organized as explicitly referenced logical documents. A project may keep them in one physical file or many files, but meaning does not depend on filenames or automatic directory scanning. How included documents and environment overrides merge remains to be decided.

Committed configuration contains secret references, never production secret values. Local environments may reference process environment variables, ignored local files, or a local secret provider. Plans, deployment history, and diagnostics must not reveal resolved values.

### Environment lifecycle

An ephemeral environment has an explicit expiry or destruction condition. Expiry initiates automatic destruction, may be renewed explicitly, and still honors each stateful component's retention policy. Failed cleanup remains visible and retryable rather than being treated as successful destruction.

## Required deployment scenarios

At minimum, the product model must be able to express:

1. The current machine using systemd with a local database and cache.
2. Another machine on the local network or VPN, accessed remotely, using systemd with local data services.
3. An EC2 instance using systemd with host-local database and cache services.
4. An EC2 instance using systemd with AWS-managed database and cache services.
5. ECS or Fargate services with AWS-managed supporting services.
6. Lambda for workloads compatible with event-driven serverless execution.
7. Hybrid environments where different components use different execution options.

## Blue-green expectation

Blue-green is a product requirement where the selected implementation can provide it safely. The meaning differs by component role:

- Request-serving services switch new traffic between revisions.
- Realtime services must account for long-lived connections and draining.
- Workers must hand off new work without losing in-flight jobs.
- Schedulers must avoid duplicate execution during a transition.
- Databases and caches require state-aware migration or endpoint-cutover behavior and cannot be treated like stateless processes.

The product must expose when a requested guarantee is unavailable instead of silently degrading it.

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

## Not yet decided

No implementation or technical architecture has been selected. In particular, the repository does not yet decide whether the product is a CLI, service, library, daemon, or combination of these; how it provisions resources; how configuration is represented; or how deployment state is stored.
