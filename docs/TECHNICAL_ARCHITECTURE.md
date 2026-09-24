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

### Fenced execution leases

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

Provision is not an application-artifact registry. Revision manifests reference immutable OCI images or executable bundles in authenticated external registries, HTTPS locations, or object stores and identify them by digest. Each execution target keeps a content-addressed cache and stages only missing digests, either by fetching the immutable external source on the target or through a future selected transfer path. The engine stages artifacts into ECR or S3 only when an AWS target requires it. Digest and policy evidence are checked again before execution.

### Signed Provision releases

Provision is distributed as signed, versioned archives for supported platforms with checksums and a software bill of materials, plus a matching OCI runner image. Package-manager integrations may wrap those releases later, but they are not the authoritative artifact. The initial release has neither a mandatory installer daemon nor an automatic updater.

### Policy-rooted retention and garbage collection

Retention is explicit. Environment heads, approved Plans, live or resumable operations, retained generations, rollback windows, audit holds, and recovery records are protected roots. Snapshots may compact reads without rewriting journal events. Unreferenced content-addressed objects are collected through mark, quarantine, and delayed deletion. Local state defaults to no automatic deletion; an installation must opt into time-based expiry consistent with its audit and recovery policy.

### Explicit trust boundaries

Authenticated configuration and approved Plans express authorized intent but do not make executable inputs safe. Artifacts, Actions, provider responses, remote hosts, and network inputs are treated as untrusted. The engine verifies digests, isolates Actions where the target permits, grants short-lived least-privilege credentials, redacts secrets structurally, and requires an explicit approved fallback when a requested isolation guarantee is unavailable.

### Native and OCI host workloads

The host execution implementation accepts digest-addressed executable bundles and digest-addressed OCI images. Executable bundles run directly under hardened systemd units. OCI images run through a declared container runtime while systemd remains their lifecycle manager. This supports lightweight native development and container-first applications without turning the host target into a cluster abstraction.

### Curated AWS service portfolio

The initial AWS supporting-service portfolio is RDS for PostgreSQL, ElastiCache for Valkey, SQS Standard and FIFO, S3, and EventBridge Scheduler. Its execution and endpoint portfolio is ECS or Fargate, Lambda versions and aliases, Application Load Balancers for compatible HTTP and realtime workloads, API Gateway HTTP and WebSocket APIs for Lambda-oriented endpoints, and Route 53 plus ACM for authorized application records and certificates. The Configuration Compiler rejects a key-value store without an explicit Store Data Role; no adapter infers that role from the provider. ElastiCache is one possible implementation regardless of whether that role is authoritative, derived, or ephemeral.

Native ECS blue-green deployments are preferred for container services, and Lambda aliases are the stable handoff point for functions. Capability contracts remain narrower than service names: RDS Blue/Green eligibility is validated against its PostgreSQL limitations. ElastiCache for Valkey may hold derived data or application state such as sessions; it is not restricted to rebuildable caches. An authoritative deployment must observe its actual durability mode, eviction policy, recovery capability, and transition path. Eligible Valkey clusters can provide synchronous durable writes, while provider-managed scaling or upgrades may replace and synchronize nodes. Those runtime and availability properties do not alone prove the isolated candidate, cutover, retained generation, and rollback guarantees of required store blue-green. A separate-cluster transition must prove synchronization and lossless forward cutover for authoritative data; unsupported combinations fail required mode without prohibiting an explicitly approved weaker rollout.

### Provider-native workload observability

Provision emits structured management events and records references to verification evidence, but it is not a monitoring backend. Host workloads use journald and AWS workloads use CloudWatch by default. Health gates consume explicit probes and selected metrics. OpenTelemetry export may be supported, but deployed workloads never require a Provision-hosted telemetry service to continue operating.

### Curated systemd host runtime

The curated OCI runtime on Linux hosts is rootless Podman with Quadlet, managed by systemd user units under a dedicated environment account. Bootstrap enables lingering and prepares storage, networking, and permissions. Native executable bundles use hardened systemd units. Other container runtimes require distinct implementations and capability contracts.

### Host-local supporting services

The managed host-local database is PostgreSQL and the managed key-value store is Valkey, each packaged as a digest-pinned OCI image with a distinct data directory per physical generation. Host-installed instances can be bound as external components. Supported product versions follow a tested moving compatibility window, not an unbounded promise. PostgreSQL side-by-side transitions use replication where compatible and explicitly verify schema, DDL, and sequence constraints. Derived Valkey data may be rebuilt from a declared source; authoritative Valkey data must be preserved through replication and a bounded write pause when validation proves the required lossless forward cutover.

The managed host-local queue uses RabbitMQ quorum queues. Its capability contract covers acknowledgements, publisher confirms, retry, dead-letter handling, retention, and at-least-once delivery. A generation transition may use Shovel with destination confirmation before source acknowledgement, followed by a fenced producer and consumer handoff. Ordering and deduplication are advertised only when the selected queue topology can prove them.

The initial host-local object store exposes an application-visible filesystem directory rather than the S3 protocol. The Configuration Compiler validates this access method against the application's declared supported methods; Provision does not insert a transparent filesystem-to-S3 translator. Each physical generation has a separate directory. Transition work stages and verifies a copy, pauses writers within a declared bound for final synchronization, then rebinds application workloads and retains the old generation. Applications needing an S3-compatible protocol may bind an external service. Capability validation must reject required mode when the filesystem, writer control, or rebinding path cannot prove lossless forward cutover.

### Host endpoint routing and scheduled execution

Caddy is the curated host router for HTTP, HTTPS, and WebSocket endpoints. Provision owns its generated JSON configuration and atomically loads a complete new configuration. Candidate workloads listen on private generation-specific ports. Caddy switches new traffic, while role-specific drain policy governs old requests and WebSocket connections. A realtime rollout has a declared maximum drain period; remaining connections are closed at its deadline and clients must reconnect. Socket migration and zero disconnections are not claimed.

Stable systemd timers invoke a pinned, nonresident `provision runtime schedule` applet. A local SQLite occurrence ledger lets the applet enforce timezone, daylight-saving, overlap, retry, missed-run, and bounded catch-up behavior before launching a generation-specific task unit. The timer and applet keep schedules active when the management CLI is absent; only the applet version changes through an auditable Plan.

### Host qualification and privilege

A host is qualified through observed capabilities: systemd, cgroup v2 for the curated OCI path, rootless Podman and Quadlet, journald, suitable filesystem semantics, and required networking tools. Ubuntu LTS and Amazon Linux 2023 are the first certified distributions on amd64 and arm64. Bootstrap requires root or sudo, installs a root-owned nonresident host executor, and creates dedicated environment accounts. The tool used to prepare the host is not selected: Ansible may be tested, but Provision must not depend on it unless that test justifies the extra layer. Later operations pass typed operation bundles to the restricted entrypoint rather than granting arbitrary shell or sudo access. SSH authentication alone does not authorize deployment: the executor also verifies a short-lived authorization for the exact approved Plan, target environment, and host before accepting operations. The exact authorization encoding, allowlist, transport, replay protection, and filesystem rules remain to be specified and tested.

### Explicit builds and revision assembly

Application-defined builds are Actions with declared inputs and named digest-addressed artifact and evidence outputs. Every artifact has an explicit publication destination; the publishing operation verifies the digest at that destination. Target-specific staging into ECR, S3, or a host cache is distinct from publishing the authoritative artifact. A deterministic `revision assemble` operation accepts those outputs, including outputs from separate repositories or existing pipelines, and creates a complete immutable application revision manifest. Deployment consumes that manifest; it does not infer artifacts from the invoking process's current checkout or run a build implicitly.

The compiler, planner, and executor do not detect a framework or synthesize Laravel- or PHP-specific commands. A framework may supply optional templates or Actions, but the same artifact, health, binding, and rollout contracts apply to applications built without that framework.

### Explicit configuration references

One root configuration document refers to typed application, environment, implementation, policy, and revision documents by explicit local relative paths. The first release has no implicit directory merge, remote include, template language, or environment-variable substitution. The compiler's versioned schema generates JSON Schema and reference documentation.

### Curated secret delivery

Local automation may reference environment variables or protected files. AWS adapters resolve AWS Secrets Manager and SSM Parameter Store references. Host workloads receive systemd credential files; ECS workloads receive provider-native secret references. Lambda workloads normally fetch secrets at runtime with scoped IAM. Materializing a secret into a Lambda environment variable or ordinary process environment is an explicit weaker-capability mode. Resolved values remain outside Plans, journals, and evidence.

### Binary and runtime compatibility

Every Provision binary declares the configuration, Plan, snapshot, journal-event, operation-kind, and installed-runtime-asset versions it can handle. It reads the previous major persisted format during a documented migration window. Runtime applets are pinned per environment and update only through an auditable Plan. Upgrading an operator CLI never implicitly changes installed runtime assets.

### Capability proof at plan time

Each adapter publishes a versioned tested capability catalog and refines it using observations of the exact product version, host, AWS region, account features, resource configuration, quotas, and workload requirements. The resulting capability evidence is embedded in the immutable Plan. Unknown or unverified capability cannot satisfy a required guarantee.

### Host workload generations

Native and Podman workloads install each revision in a separate release directory and systemd unit. A candidate HTTP or realtime workload starts on a private generation-specific port and passes its readiness and verification gates before Caddy switches new traffic. The previous unit stays available through its rollback window. Caddy and the runtime apply role-specific draining to old HTTP requests and WebSocket connections. Workers use a different handoff: stop old consumers from claiming new work, observe and drain or release in-flight jobs, then admit candidate consumers. Required mode validates that the selected queue and workload can expose and control that sequence.

### Host PostgreSQL and Valkey transitions

A managed PostgreSQL transition prepares a separate candidate cluster and replicates while the active cluster serves traffic. Physical replication handles compatible changes; logical replication is eligible only after schema, DDL, sequence, and workload restrictions pass validation. A bounded write fence permits final synchronization and verification before bindings switch. Once new writes reach the candidate, return to the old generation is forward-only unless a separately proven reverse-synchronization path meets the selected rollback guarantee.

For a Valkey-backed key-value store, the declared Store Data Role selects the transition. Derived data is rebuilt from its declared authoritative source and verified. Authoritative data uses replication, a bounded write fence, and matching replication evidence before rebinding. Asynchronous replication alone cannot establish lossless forward cutover. Each transition must also satisfy the store's separate failure-durability and recovery policies.

### Queue generation transitions

Changing only workers leaves their Queue generation intact. Replacing a managed Queue creates a candidate physical generation while preserving one logical Queue. The operation fences new producer admission to the old generation, drains or releases in-flight messages, transfers the remainder with destination confirmation, verifies the old generation, then rebinds producers and consumers. RabbitMQ may use a Shovel that acknowledges the source only after destination confirmation. SQS requires an explicit application-aware mover; there is no assumed atomic queue transfer. The resulting guarantee is at-least-once, and required mode fails if producer fencing, message preservation, or the Queue's declared ordering requirement cannot be proven through handoff.

### Object store transitions

A filesystem-backed or S3-backed object store transition creates a separate candidate generation, inventories object versions and deletions, stages and verifies an initial copy, controls all writers, performs final synchronization within a declared write pause, switches application bindings, and retains the old generation. S3 live replication and Batch Replication may accelerate copying, but asynchronous replication status does not alone prove an exact cutover. Required mode needs verified writer control and final parity for the workload's object semantics.

### RDS transition orchestration

Provision uses RDS Blue/Green switchover when the observed PostgreSQL resource is eligible. The adapter validates prerequisites, records the exact provider transition, observes guardrails and completion, and verifies application connectivity afterward. It does not perform a competing database traffic switch. The previous RDS generation remains recovery material, but once the candidate accepts writes it does not alone provide zero-loss rollback.

### ECS and Lambda workload handoff

Routed ECS HTTP workloads use native ECS blue-green deployment with candidate verification and the declared bake window. Headless ECS workers require Provision's explicit consumer gate because ECS cannot shift their work intake through a load balancer. Lambda versions and aliases serve as stable handoff points for eligible HTTP and Task handlers; SQS event-source mappings require pause, drain, and resumption checks. The initial Lambda-oriented WebSocket implementation advertises a narrower rollout guarantee unless it can pin an established connection to its prior handler revision and drain that revision; it cannot claim the persistent-server connection handoff by default.

### Fenced schedule handoff

One stable trigger serves each logical Schedule. Its occurrence ledger records the active generation and fencing token. A handoff changes that generation through one recorded operation, and each due occurrence is recorded before invocation with a stable invocation identity reused across retries. Tasks remain idempotent because a crash at the delivery boundary can cause a duplicate.

### Plan-bound host authority

The deployment principal may invoke only the root-owned host executor entrypoint. The implemented local and SSH paths share one Operation Handler contract: acquire a renewable fenced execution lease for the Environment mutation, journal intent, and sign an Ed25519 authorization with a maximum five-minute lifetime. This is separate from the optional team-member Environment Lease in the product model. The proof binds the exact approved and current Plan, Application, Environment, Host Target, operator, observed executor digest, typed operation digest, attempt, and fencing token. The executor verifies the root-owned public key and bootstrap record, rejects replays and stale fencing tokens durably, and currently permits only the dependency-free `stageArtifact` operation into a fixed digest-addressed cache. It accepts no arbitrary shell command or caller-selected filesystem path. The Operation Handler observes the fixed cache before apply or resumption, verifies structured results, and exposes the operation's declared recovery mode. The State Backend conditionally commits an authoritative outcome and releases the execution lease atomically. A stale result cannot append to that journal; its submitted observation is retained in a separate non-authoritative rejection log.

Initial bootstrap remains a separate explicit operation with elevated privilege. Remote execution uses an already trusted OpenSSH host key with strict checking, batch mode, and password authentication disabled. Unknown or changed identities fail closed. Inspection records that ED25519 host-key fingerprint in the Plan, the authorization binds it, and the executor verifies the fingerprint against its own host key before accepting the envelope. SSH authentication identifies the machine and user but does not replace Plan authorization; the same signed typed envelope is verified by the remote executor. A transport loss is followed by read-only observation and becomes an explicit uncertain outcome when completion cannot be proved.

### Secret version changes

Provision records a secret's reference and observed version or equivalent non-disclosing change identity, never its resolved value. Immediately before execution it re-observes every referenced secret identity; a change since planning makes the Plan stale and requires replanning, whether or not revocation has occurred. Rotation verifies a consumer's dynamic refresh behavior or creates a new environment configuration revision and rollout for affected workloads. Unrelated components are not restarted simply because one reference rotated. The change-detection mechanism for unversioned local secret sources remains implementation work.

### Host-only backups and restore proof

Every managed-store adapter owns the backup and isolated restore-verification capabilities it advertises; custom Actions may add application-consistency steps but do not replace a required baseline adapter capability. Authoritative host-local stores back up outside the active physical generation. When the recovery policy requires protection from host loss, the destination is off-host. The initial destination types are authenticated filesystem and SSH-accessible locations, so host-only environments do not need AWS. Scheduled restore verification creates an isolated candidate generation and records the achieved recovery point and recovery time against policy.

### Curated build execution

The curated build Action path uses an ephemeral, unprivileged OCI environment with declared inputs, network access, secrets, and named digest-addressed outputs. Existing CI may publish equivalent artifacts and evidence for revision assembly. A native executable build is available only when the selected target and policy explicitly accept its weaker isolation.

### Published compatibility matrix

Every Provision release publishes a versioned support matrix covering certified host distributions, systemd and Podman capabilities, database, key-value-store and queue versions, AWS features, and runtime-asset compatibility. A version enters managed support after its adapter contract tests pass. Planning combines this matrix with current observations; an unsupported version may still be bound externally but cannot inherit a managed guarantee.
