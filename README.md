# Provision

Provision is a working product concept for describing and deploying web applications across local machines, remote development hosts, and cloud execution environments.

Its primary users are application teams without dedicated platform teams. The intended scope runs from development through small and medium production deployments.

## Status

This repository is in **product discovery**. It records the product model and requirements discussed so far. It does not contain an agreed implementation or technical architecture.

The current goal is to understand what users need to express before choosing configuration syntax, internal architecture, provisioning technology, or runtime behavior.

## Product direction

The product should let a user describe:

- an application and its components;
- multiple environments with different behavior and policy;
- where each component should run;
- alternative implementations for databases, caches, realtime services, queues, workers, tasks, and schedules;
- local, remote-host, EC2, ECS, Lambda, and managed-service deployment combinations;
- blue-green behavior wherever the selected implementation can safely provide it.

## Documentation

- [Product model](docs/PRODUCT_MODEL.md)
- [Domain language](CONTEXT.md)
- [Open questions](docs/OPEN_QUESTIONS.md)
- [Decision records](docs/adr/)
- [Exploratory material](docs/explorations/README.md)

## Important boundary

The material under `docs/explorations/` contains configuration and diagram sketches created to test the product model. Those sketches are not architecture decisions or normative specifications.
