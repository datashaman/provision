# Provision

Licensed under the [MIT License](LICENSE).

Provision is a working product concept for describing and deploying web applications across local machines, remote development hosts, and cloud execution environments.

It is the deliberate successor to Gimme, not an automatic upgrade of running Gimme deployments. Proven behavior and acceptance evidence inform Provision; existing deployments remain Gimme-owned until a separately verified migration.

Unlike Gimme, Provision's core is not Laravel-oriented. Application artifacts and declared Actions supply framework-specific behavior; the deployment model and required guarantees work without Laravel or PHP.

Its primary users are application teams without dedicated platform teams. The intended scope runs from development through small and medium production deployments.

## Status

The **product model is agreed**, with explicit later amendments recorded in decision records. Foundational technical-architecture decisions are recorded, and implementation planning is under way.

The current goal is to turn the agreed model into executable acceptance cases and an end-to-end host deployment slice without weakening its guarantees.

The current Go increment validates a strict single-HTTP configuration, verifies supplied Artifact bytes against the Revision digest, inspects Host Targets, previews a deterministic Plan, persists exact-Plan approvals in a local SQLite State Backend, and can execute the Plan's first eight typed operations on a direct-local or remote Host Target. Those operations stage the approved Artifact, install an immutable candidate Generation, start it under a separate hardened systemd unit on a loopback-only port, evaluate the complete Health Contract, atomically switch the stable Caddy Endpoint only after the host confirms that exact verification, verify the application again through that stable Endpoint, complete the Environment's declared bounded ordinary-HTTP drain, and record the exact stopped previous Generation as restartable through its declared rollback-window deadline. An interrupted or uncertain operation can be resumed from its durable journal entry and fresh host evidence under a new fencing token; ambiguous state pauses with an actionable recovery instruction instead of replaying blindly. A failed post-switch check automatically restores a directly verified retained previous Generation; if recovery cannot be proved, Provision records an explicit uncertain outcome for operator action. Rollback-window recording never deletes a unit definition, Generation directory or manifest, or Artifact; expiry and cleanup remain a separate unimplemented decision. This HTTP drain does not claim WebSocket or other long-lived-stream draining. Its non-Laravel example workload lives separately in [`datashaman/provision-example-http`](https://github.com/datashaman/provision-example-http). This repository also includes a separate, privileged disposable-host bootstrap path.

The compiler, Planner, and restricted Host executor also support the first complete asynchronous Host tracer: one managed RabbitMQ Queue, one gated systemd Worker, one generation-specific systemd Task, and one stable Schedule. An approved Plan installs immutable Worker and Task generations, verifies the Worker while intake is closed, opens intake, installs a digest-pinned nonresident Schedule runtime, and hands a stable systemd timer to the exact Task generation. Each due occurrence is committed to a SQLite ledger before Task delivery; the Task publishes with confirmation and the Worker manually acknowledges the same stable message identity. This is the initial-generation path only: Worker upgrades, schedule handoff between revisions, retry/redelivery, overlap, missed-run, crash-boundary recovery, and Queue replacement remain separate work. See [Asynchronous Host execution](docs/asynchronous-plan-preview.md) and the [live evidence](docs/evidence/2026-09-26-first-scheduled-message.md).

```sh
go run ./cmd/provision config validate --file examples/host-http/root.yaml
go run ./cmd/provision host inspect --address base.local --user marlinf
mkdir -m 0700 .provision
go run ./cmd/provision plan preview --file examples/host-http/root.yaml --state .provision/state.db
go run ./cmd/provision config validate --file examples/host-async/root.yaml
go run ./cmd/provision plan preview --file examples/host-async/root.yaml
```

Preview output contains the Plan digest needed by the approval flow. Supplying `--state` persists the exact preview as the Environment's current, initially unapproved Plan. See [Plan approval and status](docs/plan-approval.md) for the durable SQLite workflow and its local OS trust boundary.

The current execution slice stages one approved Artifact, prepares and verifies an isolated candidate, switches and verifies the stable Caddy Endpoint, completes a bounded ordinary-HTTP drain, declares exact rollback-window retention, resumes interrupted operations, and restores the retained previous Generation when post-switch verification fails on either a direct-local or remote Host Target. See [Authorized release preparation](docs/release-preparation.md) for key setup, trusted SSH identity, bootstrap, execution, recovery, journal inspection, and the exact safety boundary.

Download the example application from its own release and pass it to validation to verify its actual bytes:

```sh
gh release download v0.1.0 --repo datashaman/provision-example-http --pattern provision-example-http-linux-amd64.tar.gz --dir /tmp/provision-example-http
go run ./cmd/provision config validate --file examples/host-http/root.yaml --artifact-file /tmp/provision-example-http/provision-example-http-linux-amd64.tar.gz
```

The local file is a read-only verification input, not a deployment-source override. See the [host bootstrap guide](docs/host-bootstrap.md) before preparing a disposable machine. Never apply the bootstrap over a Gimme-managed host.

Development uses an optional exact Go tool pin in `mise.toml`; mise is not a runtime dependency of Provision or deployed applications.

## Product direction

The product should let a user describe:

- an application and its components;
- multiple environments with different behavior and policy;
- where each component should run;
- alternative implementations for databases, key-value stores, object stores, realtime services, queues, workers, tasks, and schedules;
- local, remote-host, EC2, ECS, Lambda, and managed-service deployment combinations;
- blue-green behavior wherever the selected implementation can safely provide it.

## Documentation

- [Product model](docs/PRODUCT_MODEL.md)
- [Technical architecture](docs/TECHNICAL_ARCHITECTURE.md)
- [Gimme successor plan](docs/GIMME_SUCCESSOR_PLAN.md)
- [Domain language](CONTEXT.md)
- [Open questions](docs/OPEN_QUESTIONS.md)
- [Disposable host bootstrap](docs/host-bootstrap.md)
- [Plan approval and status](docs/plan-approval.md)
- [Authorized release preparation](docs/release-preparation.md)
- [Asynchronous Host execution](docs/asynchronous-plan-preview.md)
- [Direct-local failure matrix](docs/direct-local-failure-matrix.md)
- [Remote SSH failure matrix](docs/remote-ssh-failure-matrix.md)
- [Test-backed support matrix](docs/SUPPORT_MATRIX.md)
- [Decision records](docs/adr/)
- [Exploratory material](docs/explorations/README.md)

## Important boundary

The material under `docs/explorations/` contains configuration and diagram sketches created to test the product model. Those sketches are not architecture decisions or normative specifications.

Technical architecture must conform to the agreed product model and decision records. A product constraint may be changed only through an explicit product decision, not as an incidental implementation compromise.
