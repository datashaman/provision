# Select managed Host RabbitMQ packaging

**Status: Accepted — 2026-09-25.**

## Context

The first asynchronous Host tracer needs one managed RabbitMQ node and one
single-member quorum Queue. Packaging is part of the capability boundary: a
Plan must bind an immutable product identity, a service identity, owned paths,
credentials, observed topology, and the exact restart and upgrade behavior it
relies on. The choice must not turn a Queue upgrade into an unrecorded host
package side effect or imply that a single-node quorum Queue tolerates host
loss.

The clean Ubuntu 26.04 acceptance image offers two viable starting points:

- Ubuntu's native `rabbitmq-server` package, version `4.0.5-10ubuntu5`, as one
  host-wide system service; or
- Ubuntu's Podman package plus a rootless Quadlet under the dedicated
  Environment account, running an architecture-specific RabbitMQ OCI manifest
  by digest.

The evidence and comparison are recorded in
[`2026-09-25-rabbitmq-packaging-comparison.md`](../evidence/2026-09-25-rabbitmq-packaging-comparison.md).

## Decision

Package the managed Host RabbitMQ implementation as a digest-pinned OCI image
run by rootless Podman and a Quadlet user unit under the dedicated Environment
account. Keep systemd as the lifecycle manager. Bind both the image index
identity and the resolved target-platform manifest digest in Plan evidence,
and refuse tag-only image references.

Treat Podman, Quadlet support, subordinate ID mappings, the lingering user
manager, root-owned Quadlet definition, Environment-owned durable data
directory, and encrypted systemd credential delivery as explicit Host
bootstrap capabilities. Pre-pull and verify the approved manifest before the
service operation; service start must never select or update a mutable tag.

Scope this first topology to one RabbitMQ node and one quorum member. It can
prove durable restart behavior, publisher confirms, consumer acknowledgements,
and at-least-once processing, but it has zero node- or host-failure tolerance.
Planning must reject any availability intent that requires such tolerance.

An OCI digest makes the broker executable identity immutable; it does not make
Queue data backward-compatible. Every RabbitMQ upgrade remains a separately
planned operation with an observed compatibility decision. Automatic image
rollback is forbidden after an incompatible data-format or feature transition.

## Decision rationale

The rootless Quadlet path gives each Environment its own service identity,
version, network boundary, and durable paths. It avoids a host-wide package
transaction and shared Erlang runtime becoming an implicit mutation of every
managed Environment on the machine. It also matches the existing decision to
use rootless Podman and Quadlet for curated OCI Host workloads.

The native package remains a technically viable external or future curated
implementation. It has the simpler bootstrap and direct systemd integration,
but the observed Ubuntu package is host-wide, older than the current RabbitMQ
release, and supplied by a distribution repository that RabbitMQ explicitly
advises against for current releases. Making it safely managed would require
binding the full RabbitMQ and Erlang package closure, repository keys and
origins, package holds, shared-host ownership, and upgrade consequences in
every affected Plan.

## Consequences

- Host bootstrap grows to install and observe Podman/Quadlet and prepare the
  Environment account for rootless boot-persistent services.
- The accepted packaging path was qualified on a freshly restored acceptance
  VM. The proof bound the exact image, delivered an encrypted user-scoped
  systemd credential, asserted owned paths and strict inventory isolation,
  exercised publisher confirms and manual acknowledgements, and survived a
  full Host reboot with the same durable message identity.
- The restricted executor receives only narrow typed operations for the
  approved Queue lifecycle. It does not receive arbitrary Podman access.
- The support matrix records the exact Host OS, systemd, Podman, RabbitMQ image
  index and platform manifest, architecture, topology, executor, and test
  result. Nearby versions inherit no guarantee.
- Native RabbitMQ can still be bound as an External Component after its
  identity and capabilities are observed; this decision does not silently
  manage it.

## Rejected alternative

The Ubuntu-native `rabbitmq-server` package is not selected for the curated
managed implementation. Its host-wide package and Erlang dependency closure
would couple Environment ownership and upgrades to shared Host state. It may
still be observed as an External Component, and a future curated native
implementation would require its own decision and qualification evidence.

## Acceptance evidence

The operator approved the rootless OCI/Quadlet recommendation before any live
mutation. The accepted path then passed the issue-specific proof on a freshly
restored Ubuntu 26.04 `x86_64` VM:

- Podman `5.7.0+ds2-3build1` ran RabbitMQ `4.3.6` from Linux `amd64` manifest
  `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`;
- rootless service identity `provision-lab`, unit
  `provision-lab-rabbitmq.service`, and one-member quorum Queue
  `provision-issue36` matched the declared topology;
- publisher confirms, manual consumer acknowledgements, encrypted credential
  delivery, read-only credential mounting, tmpfs runtime handling, and absence
  of a durable plaintext password all passed;
- the unit restarted automatically after a real Host reboot and the same
  confirmed message ID was consumed and acknowledged afterward; and
- package, image, container, and Quadlet inventories remained exact, while
  native RabbitMQ remained absent.

The structured non-secret observation is retained with the
[qualification evidence](../evidence/2026-09-25-rabbitmq-packaging-qualification.json).
This acceptance selects packaging only; it does not enable a production Queue
executor operation.
