# Gimme → Provision successor plan

**Status:** Implementation planning. [ADR 0071](adr/0071-treat-provision-as-gimmes-successor.md) establishes the successor relationship; this plan does not change the agreed [product model](PRODUCT_MODEL.md) or [technical architecture](TECHNICAL_ARCHITECTURE.md).

Gimme source was inspected at commit `f3b98f9163e40d1b666eb1837498969aabec9c38`. Its worktree had unrelated uncommitted changes, so this assessment relies on committed behavior and documented evidence, not those edits. The source inventory is in [Gimme capability inventory](explorations/gimme-capability-inventory.md).

## What the dispositions mean

- **Retain** the behavioral guarantee and its acceptance evidence. This does not mean copying Python, PHP, or shell implementation into the Go engine.
- **Adapt** a proven idea to Provision's different domain model or execution contract, then verify it again through Provision's interfaces.
- **Replace** an implementation or storage format that conflicts with the agreed architecture; Gimme continues operating existing deployments until an explicit migration.
- **Build** a capability Gimme does not currently implement. A proposal or inert renderer is not treated as working behavior.

## Capability map

| Gimme area | Disposition for Provision | Required proof or deliberate difference |
| --- | --- | --- |
| Existing Ubuntu Target identity and host qualification | Adapt | Keep explicit external host ownership and observed capabilities. Provision's Host Target covers the current Linux machine and a remote LAN, VPN, or EC2 systemd host; VM creation remains outside the agreed first-release boundary. |
| Interactive bootstrap and restricted privileged helpers | Retain safety contract; replace implementation | Preserve the separation between initial privilege grant and ordinary plan-bound operations. The Go host executor must verify the exact approved Plan, environment, and host, and accept typed operations only. |
| Exact Target APT stack and host service reconciliation | Adapt | Curate and test the systemd, networking, router, and runtime prerequisites. Do not expose unrestricted package, service, or shell inputs. |
| Deployer transport, Python/MCP orchestration, and Laravel-specific recipes | Replace | One Go Configuration Compiler, Planner, and Executor serve local CLI, SSH, and AWS paths. Framework behavior moves into declared artifacts and bounded Actions rather than a second control-plane core. |
| Content-addressed plan/apply, stale-plan rejection, and executable-input fingerprinting | Retain safety contract; adapt evidence | Canonical Plans bind revisions, observed state, capability evidence, artifact and Action digests, and executable versions. Recompute preconditions immediately before execution and test stale rejection. |
| Local JSON desired/operational state | Replace | Provision separates declared configuration from journaled Recorded State and uses the agreed SQLite and DynamoDB/S3 State Backend adapters. No implicit import of Gimme state. |
| mise exact runtime pins | Adapt, pending a specific host-runtime decision | Keep reproducible tool selection for developer and build workflows. Decide separately whether a native host workload may use a pinned per-environment mise runtime; never make mise an undeclared dependency of every production artifact. |
| Caddy routing, isolated candidates, sticky weighted rollout, and failure tests | Retain acceptance evidence; adapt behavior | Prove Provision's required HTTP and realtime handoffs, health gates, drain, rollback, and interrupted-operation recovery. Weighted canary traffic is not silently assumed to be a first-release Provision guarantee. |
| Host-local PostgreSQL and Valkey resources | Adapt model; replace transition mechanics | Preserve explicit bindings and version observation, but prove side-by-side Store Generations, lossless forward cutover, rollback classification, and isolated restore rather than treating a single host installation as blue-green. |
| AWS RDS PostgreSQL and ElastiCache Valkey | Adapt provider knowledge; replace adapter code | Port validation and failure cases to Go AWS adapters. Re-prove each claimed transition against the exact AWS account, region, engine version, topology, and Store Data Role; existing Gimme support does not certify Provision blue-green. |
| Laravel worker and scheduler process management | Adapt | Keep tested ownership and handoff behavior while representing Worker, Task, Queue, and Schedule as independent logical roles instead of Laravel process settings. |
| Artifact and recovery storage | Adapt | Preserve digest identity and recovery evidence. Provision requires complete application revisions, declared publication destinations, adapter-owned backup capability, and isolated restore verification. |
| First-class Queue, application Object Store, general realtime roles, ECS/Fargate, and Lambda | Build | These are Provision release requirements or curated implementations, not capabilities inherited merely because Gimme discusses them. Each needs a capability contract and an end-to-end test. |
| Ansible Target-stack renderer | Do not count as live behavior | Gimme's renderer is pure and non-mutating. Provision may later evaluate Ansible for bounded host preparation, but no design or release claim may assume that Gimme has an Ansible apply path. |

## Implementation sequence

1. **Transfer acceptance evidence, not operational ownership.** Convert Gimme's host bootstrap, plan-staleness, secret-redaction, runtime-pin, and rollout failure cases into Provision-facing specifications. Create a small application fixture that exercises each first-class role. Do not point Provision at a Gimme-managed Caddy site, service unit, or store yet.
2. **Prove the local and remote host tracer.** Compile one configuration into a Plan, approve it, journal execution, deploy an HTTP candidate through systemd and Caddy, verify the switch, inject failure, and resume or roll back. Run the same Provision interfaces directly on Linux and over SSH.
3. **Prove stateful host transitions.** Add PostgreSQL, authoritative and derived Valkey, Queue, filesystem Object Store, backup, and isolated restore. Inject failure before and after each side effect. A `required` guarantee fails closed if the evidence cannot support it.
4. **Extend the same engine to AWS.** Validate the default profile's account identity, select one region and a spend limit, inventory existing resources, and use uniquely tagged disposable resources. Add EC2-host, RDS, ElastiCache, SQS, S3, ECS/Fargate, Lambda, and hybrid adapters without a second planning engine.
5. **Gate the first useful release.** All seven product scenarios execute end to end against disposable targets. The published support matrix identifies exact tested versions and honest blue-green capability limits.

## Migration and lab guardrails

- The running `weftwise` deployment remains Gimme-owned. Neither tool may simultaneously manage the same routing, systemd unit, database, key-value store, secret, or backup identity.
- `base.local` is a disposable host for testing, but resetting or changing its current Gimme deployment is a separate explicit action. Keep it intact until the Provision host tracer and failure tests are ready.
- Gimme's source and tests are evidence, not a trusted state-import format. Any future Gimme-to-Provision migration needs an explicit inventory, ownership transfer, data backup and restore proof, route cutover, rollback classification, and approval.
- Do not assume Gimme's local or AWS behaviors satisfy Provision's stronger blue-green requirements without new verification.

## Decisions still needed before the relevant slice

- Whether VM creation and deletion should ever become a managed Provision lifecycle; the current product model deliberately treats hosts and EC2 instances as external targets.
- Whether mise is an optional native-host runtime implementation as well as a developer/build tool, and how its exact binary and runtime versions enter artifact and Plan evidence.
- Whether an existing Gimme deployment needs a supported in-place migration path, or whether Gimme remains an incumbent while new Provision environments are created independently.
- The AWS experiment region and spending limit before any paid resource is created.
