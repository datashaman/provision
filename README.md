# Provision

Provision is a working product concept for describing and deploying web applications across local machines, remote development hosts, and cloud execution environments.

Its primary users are application teams without dedicated platform teams. The intended scope runs from development through small and medium production deployments.

## Status

The **product model is agreed**, with explicit later amendments recorded in decision records. Technical-architecture discovery is under way, and foundational architecture decisions are recorded in this repository.

The current goal is to choose configuration syntax, system shape, state model, provisioning approach, and runtime mechanisms without weakening the agreed product guarantees.

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
- [Domain language](CONTEXT.md)
- [Open questions](docs/OPEN_QUESTIONS.md)
- [Decision records](docs/adr/)
- [Exploratory material](docs/explorations/README.md)

## Important boundary

The material under `docs/explorations/` contains configuration and diagram sketches created to test the product model. Those sketches are not architecture decisions or normative specifications.

Technical architecture must conform to the agreed product model and decision records. A product constraint may be changed only through an explicit product decision, not as an incidental implementation compromise.
