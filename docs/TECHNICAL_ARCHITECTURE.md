# Technical architecture

**Status: Discovery.** Foundational architecture decisions are agreed; further implementation decisions remain open.

## Inputs

Architecture must satisfy:

- the agreed product model in `docs/PRODUCT_MODEL.md`;
- the canonical domain language in `CONTEXT.md`;
- the decisions recorded in `docs/adr/`.

Product constraints are not implementation defaults. If an architecture cannot satisfy one, the conflict must return to product design as an explicit decision rather than being silently weakened.

## Architectural constraints already imposed by the product

- Core local and remote-host workflows cannot require a hosted control plane.
- Deployed workloads continue operating when Provision is unavailable.
- Configuration is declarative and may reference bounded custom Actions.
- Plans are immutable and tied to exact revisions, observed state, capabilities, and artifact digests.
- Recorded operational state is distinct from declared intent.
- Operations are resumable and auditable but are not presented as distributed transactions.
- Implementations expose explicit capability contracts and cannot silently degrade required guarantees.
- The first release targets current or remote Linux systemd hosts and AWS.
- The seven required deployment scenarios must execute and be verified end to end.

## Decisions

### Local-first engine

Provision's behavioral core is a local engine invoked by a CLI. Basic current-host, remote-host, and AWS workflows require neither a resident daemon nor a hosted account. An optional coordinator and remote runner may later provide shared state, approvals, automation, and delegated execution, but they call the same engine rather than reimplementing planning or execution.

### Configuration documents

Users author versioned YAML documents under a restricted YAML 1.2 profile and strict schema. Equivalent JSON is accepted for generated input. Unknown fields are errors, includes are explicit, and parsing produces a canonical internal model with source provenance. Configuration does not execute code; it references immutable Actions.

### Deep core modules

The initial architecture has three primary deep modules:

1. **Configuration Compiler** — documents to a validated canonical model with provenance.
2. **Planner** — canonical desired model, observed state, and capability contracts to an immutable Plan.
3. **Executor** — approved Plan, execution journal, and implementation adapters to a resumable result.

The CLI, future coordinator, automation, and tests use these same interfaces. Provider and runtime variation sits behind adapter seams rather than leaking into callers.

### Plans are canonical data

A Plan is canonical serializable data containing typed operations, dependencies, preconditions, approvals, expected observations, sensitive-value references, recovery behavior, and deterministic identity. Custom Actions are referenced by digest and contract. Plans do not embed arbitrary closures or generated executable scripts.

### Go implementation

The local engine, CLI, and future runner are implemented in Go. Go supports the required desktop and Linux distribution targets, provides the official AWS SDK, and suits a single distributable operational binary with typed internal models and explicit concurrency.

### Journaled state behind a State Backend seam

Each environment has an append-only execution journal and immutable state snapshots for efficient reads. A deep State Backend module hides persistence and exposes a small interface for loading the current snapshot, conditionally appending journal entries, acquiring or renewing a fenced lease, storing immutable Plans and evidence, and compare-and-swapping the active snapshot.

The initial architecture includes two adapters because this seam has real variation: SQLite for personal or local environments and a DynamoDB-and-S3 adapter for shared AWS environments.

### Fenced environment leases

Only one mutating operation may execute against an environment at a time. A renewable lease carries a monotonically increasing fencing token; every conditional journal append verifies the token. Reads and planning may occur concurrently, but execution revalidates state after acquiring the lease. Expired work may be resumed or explicitly superseded, while a stale executor cannot commit further progress.

### Explicit push operations

Environment changes follow an explicit observe, plan, approve, execute flow. Provision has no universal pull controller and does not silently reconcile drift. Interrupted operations resume from their journal and fresh provider observations rather than assuming the initiating process remained alive. Narrow reconciliation may be opted into later but still emits recorded operations.

### Agentless host transport

One host interface has a direct local adapter for the current Linux machine and an agentless SSH adapter for remote Linux hosts. The remote adapter transfers digest-addressed artifacts, installs versioned releases, manages systemd and supporting services, and observes health. It does not require a resident Provision agent; reconnect and resume use recorded checkpoints and observed host state.

### AWS shared-state adapter

The shared State Backend uses DynamoDB for environment heads, ordered journal metadata, leases, fencing tokens, and conditional state transitions. S3 stores content-addressed Plans, snapshots, evidence, and larger immutable records. Both use KMS encryption. Journal commits use DynamoDB transactions conditioned on the current fencing token and state version, while DynamoDB heads reference immutable S3 object digests.

### Direct AWS provisioning

AWS implementation adapters call provider APIs through the AWS SDK for Go. Provision's canonical Plan, Recorded State, identities, and recovery journal remain authoritative. An adapter may use CloudFormation internally when a stack provides genuine leverage, but CloudFormation does not become the universal planning or state engine. Terraform and Pulumi are not embedded initially.

### Execution-time credentials

Credentials are resolved only by the executing engine or runner. AWS adapters use SDK credential configuration such as profiles, IAM Identity Center, assumed roles, and workload roles. Configuration and Plans store role or profile references rather than credentials. SSH uses the operating-system agent or referenced key material with strict host-key verification. Actions receive narrowly scoped temporary credentials and resolved secrets only for their execution lifetime. Resolved values never enter Plans, journals, or evidence.

### One binary for human and automated execution

The Go binary exposes stable human-facing CLI commands, structured JSON input and output, a streaming machine-readable event format, deterministic exit codes, and non-interactive approval inputs. A remote runner is a one-shot invocation of the same engine: it receives a Plan digest and State Backend location, resolves credentials locally, acquires the fenced lease, executes or resumes, records the result, and exits. A future coordinator dispatches these jobs rather than hosting another Executor.

### Structured custom Actions

An Action references either a container image by digest or an immutable executable artifact with an argument vector. Shell command strings are not an Action contract. Inputs and outputs are typed; secrets are injected ephemerally; and timeouts, retry classification, checkpoints, network requirements, and produced artifacts are declared. Execution adapters may run compatible Actions locally, over SSH, in ECS or Fargate, or in Lambda.

### Observable operation recovery

Every Plan operation has deterministic identity, preconditions, an idempotency or single-attempt classification, an observation method, execution and verification behavior, retry classification, and optional compensation or forward-recovery instructions. The Executor journals intent before causing side effects and records the outcome afterward. On uncertain resumption it observes the provider before retrying and derives provider idempotency tokens from operation identity where supported. An uncertain single-attempt operation pauses for explicit recovery.

### One repository and Go module

The initial product is maintained in one repository, one Go module, and one primary `provision` binary. The Configuration Compiler, Planner, Executor, State Backend, and implementation adapters are internal packages behind their agreed interfaces. A future coordinator may introduce another command in the same repository, but it does not create a second behavioral core.

### Separate planning and execution adapter seams

Provider-specific variation is divided across two related interfaces. An Implementation Adapter used by the Planner declares capabilities, observes one logical component, and translates a desired transition into typed operations. An Operation Handler used by the Executor observes an operation's current state, applies or resumes it, verifies its result, and describes its recovery behavior. An adapter registers only the operation kinds it can emit and handle. The Planner owns ordering across components; adapters own provider-specific transition knowledge.

### Explicit evolution of persisted formats

Every persisted format has an explicit schema version. Supported configuration versions compile into the current canonical model. Snapshots have deterministic migrations, while journal events remain immutable and readers support a declared compatibility window. Approved Plans are never rewritten: each records its engine compatibility range, and an incompatible Plan must be recreated by planning again.

### Shared AWS state layout

One Provision installation or administrative domain uses one DynamoDB table and one S3 bucket rather than allocating them per environment. DynamoDB partitions state by environment identity and stores the head, fenced lease, journal events, approvals, and compact indexes. S3 stores content-addressed Plans, snapshots, evidence, and large immutable payloads. A DynamoDB transaction appends each event and advances its environment head atomically; every referenced S3 object is accepted only after its digest is verified.

### Verification through module interfaces

Tests exercise the same deep-module interfaces used by production callers. The Configuration Compiler has golden tests for resolution, diagnostics, and provenance. The Planner has golden and property tests for deterministic Plans. The Executor has state-machine tests with failures injected before and after every side effect. Every State Backend passes a shared contract suite against SQLite and real DynamoDB and S3; implementation adapters pass contract tests against disposable real targets; and the seven required scenarios have end-to-end coverage. Fakes satisfy the same interfaces and do not bypass them.

### Deterministic provider simulation

A deterministic simulator satisfies the implementation-adapter and operation-handler interfaces. It exercises planning, dependency ordering, resumability, compensation, and injected failures without real infrastructure. Simulation supplements rather than replaces provider contract tests against disposable real targets.

### Compiled internal adapter registry

The initial curated implementations are compiled into the Go binary and discovered through an internal registry. The first release does not load Go dynamic plugins or third-party provider executables. If external implementations are supported later, they communicate through a versioned out-of-process protocol with capability negotiation rather than sharing Go types or process memory.

### First release without a coordinator

The first release includes the local engine and CLI, local SQLite state, shared AWS state, and one-shot runners. It does not include the optional coordinator. The agreed interfaces preserve a future place for shared authentication, dispatch, collaboration, and user interfaces, but none of those protocols are designed before the seven required deployment scenarios work end to end.

### Canonical typed-operation envelope

Plans encode operations as canonical JSON under a restricted value profile. Each operation has a small common envelope containing its schema version, deterministic identity, kind and kind version, target, dependencies, preconditions, timeout, retry safety, recovery class, expected observation, and typed payload. The Executor understands the envelope; the registered Operation Handler owns and validates the payload schema. Plans contain neither generated scripts nor opaque executable closures.

### External artifacts with digest-addressed caching

Provision is not an application-artifact registry. Revision manifests reference immutable OCI images or executable bundles in authenticated external registries, HTTPS locations, or object stores and identify them by digest. The engine keeps a local content-addressed cache, transfers only missing digests over SSH, and stages artifacts into ECR or S3 only when an AWS target requires it. Digest and policy evidence are checked again before execution.

### Signed Provision releases

Provision is distributed as signed, versioned archives for supported platforms with checksums and a software bill of materials, plus a matching OCI runner image. Package-manager integrations may wrap those releases later, but they are not the authoritative artifact. The initial release has neither a mandatory installer daemon nor an automatic updater.

### Policy-rooted retention and garbage collection

Retention is explicit. Environment heads, approved Plans, live or resumable operations, retained generations, rollback windows, audit holds, and recovery records are protected roots. Snapshots may compact reads without rewriting journal events. Unreferenced content-addressed objects are collected through mark, quarantine, and delayed deletion. Local state defaults to no automatic deletion; an installation must opt into time-based expiry consistent with its audit and recovery policy.

### Explicit trust boundaries

Authenticated configuration and approved Plans express authorized intent but do not make executable inputs safe. Artifacts, Actions, provider responses, remote hosts, and network inputs are treated as untrusted. The engine verifies digests, isolates Actions where the target permits, grants short-lived least-privilege credentials, redacts secrets structurally, and requires an explicit approved fallback when a requested isolation guarantee is unavailable.

### Native and OCI host workloads

The host execution implementation accepts digest-addressed executable bundles and digest-addressed OCI images. Executable bundles run directly under hardened systemd units. OCI images run through a declared container runtime while systemd remains their lifecycle manager. This supports lightweight native development and container-first applications without turning the host target into a cluster abstraction.

### Curated AWS service portfolio

The initial AWS supporting-service portfolio is RDS for PostgreSQL, ElastiCache for Valkey, SQS Standard and FIFO, S3, and EventBridge Scheduler. Its execution and endpoint portfolio is ECS or Fargate, Lambda versions and aliases, Application Load Balancers for compatible HTTP and realtime workloads, API Gateway HTTP and WebSocket APIs for Lambda-oriented endpoints, and Route 53 plus ACM for authorized application records and certificates.

Native ECS blue-green deployments are preferred for container services, and Lambda aliases are the stable handoff point for functions. Capability contracts remain narrower than service names: RDS Blue/Green eligibility is validated against its PostgreSQL limitations; a derived ElastiCache generation may be rebuilt, while an authoritative cache transition fails required mode unless a synchronization implementation can prove the required guarantee.

### Provider-native workload observability

Provision emits structured management events and records references to verification evidence, but it is not a monitoring backend. Host workloads use journald and AWS workloads use CloudWatch by default. Health gates consume explicit probes and selected metrics. OpenTelemetry export may be supported, but deployed workloads never require a Provision-hosted telemetry service to continue operating.
