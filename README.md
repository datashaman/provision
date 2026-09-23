# Provision

Licensed under the [MIT License](LICENSE).

Provision is a working product concept for describing and deploying web applications across local machines, remote development hosts, and cloud execution environments.

It is the deliberate successor to Gimme, not an automatic upgrade of running Gimme deployments. Proven behavior and acceptance evidence inform Provision; existing deployments remain Gimme-owned until a separately verified migration.

Unlike Gimme, Provision's core is not Laravel-oriented. Application artifacts and declared Actions supply framework-specific behavior; the deployment model and required guarantees work without Laravel or PHP.

Its primary users are application teams without dedicated platform teams. The intended scope runs from development through small and medium production deployments.

## Status

The **product model is agreed**, with explicit later amendments recorded in decision records. Foundational technical-architecture decisions are recorded, and implementation planning is under way.

The current goal is to turn the agreed model into executable acceptance cases and an end-to-end host deployment slice without weakening its guarantees.

The current Go increment validates a strict single-HTTP configuration, can verify supplied Artifact bytes against the Revision digest, and inspects existing local or SSH hosts. It includes a non-Laravel Go HTTP fixture and a separate, privileged disposable-host bootstrap path. There is still **no** executable deployment Plan or workload apply path; the installed host executor enables inspection only.

```sh
go run ./cmd/provision config validate --file examples/host-http/root.yaml
go run ./cmd/provision host inspect --address base.local --user marlinf
```

Build the fixture bundle with `examples/host-http/build.sh amd64`, then pass its output to `config validate --artifact-file examples/host-http/dist/hello-linux-amd64.tar.gz` to verify its actual bytes. The bundle source in the Revision must be an external HTTPS or OCI location; the local file is a read-only verification input, not a deployment-source override. The declared `host-http-fixture-v1` GitHub release asset is a publication target and is not yet available; external-source verification remains pending publication. See the [host bootstrap guide](docs/host-bootstrap.md) before preparing a disposable machine. Never apply the bootstrap to the Gimme-managed `base.local` until the user has completed its separate reset.

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
- [Decision records](docs/adr/)
- [Exploratory material](docs/explorations/README.md)

## Important boundary

The material under `docs/explorations/` contains configuration and diagram sketches created to test the product model. Those sketches are not architecture decisions or normative specifications.

Technical architecture must conform to the agreed product model and decision records. A product constraint may be changed only through an explicit product decision, not as an incidental implementation compromise.
