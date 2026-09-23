# Provision

Provision is a working product concept for describing and deploying web applications across local machines, remote development hosts, and cloud execution environments.

It is the deliberate successor to Gimme, not an automatic upgrade of running Gimme deployments. Proven behavior and acceptance evidence inform Provision; existing deployments remain Gimme-owned until a separately verified migration.

Its primary users are application teams without dedicated platform teams. The intended scope runs from development through small and medium production deployments.

## Status

The **product model is agreed**, with explicit later amendments recorded in decision records. Foundational technical-architecture decisions are recorded, and implementation planning is under way.

The current goal is to turn the agreed model into executable acceptance cases and an end-to-end host deployment slice without weakening its guarantees.

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
- [Decision records](docs/adr/)
- [Exploratory material](docs/explorations/README.md)

## Important boundary

The material under `docs/explorations/` contains configuration and diagram sketches created to test the product model. Those sketches are not architecture decisions or normative specifications.

Technical architecture must conform to the agreed product model and decision records. A product constraint may be changed only through an explicit product decision, not as an incidental implementation compromise.
