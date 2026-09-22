# Product model

## Problem statement

Application teams without dedicated platform teams need a consistent way to describe a web application and deploy it into materially different environments without maintaining unrelated deployment definitions for each target.

The product should preserve the application's intent while allowing each environment to choose appropriate implementations.

## Product promise

Provision's core job is to let an application team describe an application once and safely deploy it across local hosts and selected cloud targets.

The intended scope runs from development through small and medium production deployments. Production safety must be genuine, but the initial product does not promise the governance, disaster recovery, organizational policy, or operational support expected of a mission-critical enterprise platform.

Provisioning supporting services is in scope when necessary to deploy the application. Provision is not intended to become a general infrastructure-management product.

## Core concepts

### Application

An application describes the logical system being deployed. It owns component relationships and portable behavioral requirements, not provider-specific infrastructure.

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

The same built application revision should be promotable between environments without rebuilding it.

### Component

A component represents an application role. The initial vocabulary is deliberately small and fixed:

- HTTP service;
- database;
- cache;
- realtime server;
- worker;
- scheduler;
- queue or event channel.

This list is a product vocabulary, not an extensible resource-definition system.

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

### Component ownership

Every stateful or supporting component is either managed or external.

- A managed component may be created, updated, and deleted by Provision within documented limits.
- An external component exists outside Provision's lifecycle. Provision may validate it and bind the application to it but must not mutate or delete it.

Ownership is explicit per component so one environment can mix managed and external services safely.

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

Useful runtime metadata may include the environment name, stage, application revision, and active deployment color. The exact delivery mechanism is not yet decided.

## Product constraints established so far

- Component implementation is selectable per environment and per component.
- A remote development host is distinct from the machine running the deployment command.
- Persistent services are first-class, not incidental attachments to an HTTP service.
- Workers and schedulers are first-class and may use different execution implementations.
- The product must describe scaling intent without assuming one provider's vocabulary.
- Unsupported combinations must be identified before an unsafe deployment begins.

## Not yet decided

No implementation or technical architecture has been selected. In particular, the repository does not yet decide whether the product is a CLI, service, library, daemon, or combination of these; how it provisions resources; how configuration is represented; or how deployment state is stored.
