# Provision

Licensed under the [MIT License](LICENSE).

Provision is a working product concept for describing and deploying web applications across local machines, remote development hosts, and cloud execution environments.

It is the deliberate successor to Gimme, not an automatic upgrade of running Gimme deployments. Proven behavior and acceptance evidence inform Provision; existing deployments remain Gimme-owned until a separately verified migration.

Unlike Gimme, Provision's core is not Laravel-oriented. Application artifacts and declared Actions supply framework-specific behavior; the deployment model and required guarantees work without Laravel or PHP.

Its primary users are application teams without dedicated platform teams. The intended scope runs from development through small and medium production deployments.

## Status

The **product model is agreed**, with explicit later amendments recorded in decision records. Foundational technical-architecture decisions are recorded, and implementation planning is under way.

The current goal is to turn the agreed model into executable acceptance cases and an end-to-end host deployment slice without weakening its guarantees.

The current Go increment validates a strict single-HTTP configuration, verifies supplied Artifact bytes against the Revision digest, inspects Host Targets, previews a deterministic Plan, persists exact-Plan approvals in a local SQLite State Backend, and can execute the Plan's first typed preparation operation on the current machine. That operation only downloads and verifies the approved Artifact into a digest-addressed cache; it does not install or start a workload. Its non-Laravel example workload lives separately in [`datashaman/provision-example-http`](https://github.com/datashaman/provision-example-http). This repository also includes a separate, privileged disposable-host bootstrap path.

```sh
go run ./cmd/provision config validate --file examples/host-http/root.yaml
go run ./cmd/provision host inspect --address base.local --user marlinf
mkdir -m 0700 .provision
go run ./cmd/provision plan preview --file examples/host-http/root.yaml --state .provision/state.db
```

Preview output contains the Plan digest needed by the approval flow. Supplying `--state` persists the exact preview as the Environment's current, initially unapproved Plan. See [Plan approval and status](docs/plan-approval.md) for the durable SQLite workflow and its local OS trust boundary.

The first execution slice is deliberately direct-local. See [Authorized release preparation](docs/release-preparation.md) for key setup, bootstrap, execution, journal inspection, and the exact safety boundary. SSH transport for the same operation contract is the next slice.

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
- [Decision records](docs/adr/)
- [Exploratory material](docs/explorations/README.md)

## Important boundary

The material under `docs/explorations/` contains configuration and diagram sketches created to test the product model. Those sketches are not architecture decisions or normative specifications.

Technical architecture must conform to the agreed product model and decision records. A product constraint may be changed only through an explicit product decision, not as an incidental implementation compromise.
