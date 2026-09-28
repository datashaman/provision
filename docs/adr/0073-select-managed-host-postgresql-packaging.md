# Select managed Host PostgreSQL packaging

**Status: Accepted — 2026-09-28.**

## Context

The first Database tracer needs one managed host-local PostgreSQL Store
Generation. Packaging is part of the capability boundary: later Plans must bind
an immutable product identity, rootless service identity, owned generation data
paths, credential delivery, observed host prerequisites, reboot behavior, and
support exclusions before they can plan managed Database lifecycle operations.

The clean Ubuntu 26.04 acceptance image exposes two nearby paths:

- Ubuntu's native `postgresql` package, with the `18+290ubuntu1` meta-package
  candidate and `postgresql-18` package candidate `18.6-0ubuntu0.26.04.1`, as
  host-wide package state; or
- Ubuntu's Podman package plus a rootless Quadlet under the dedicated
  Environment account, running the official PostgreSQL OCI image by digest.

## Decision

Package the curated managed Host PostgreSQL implementation as a digest-pinned
OCI image run by rootless Podman and a Quadlet user unit under the dedicated
Environment account. Keep systemd as the lifecycle manager. Bind both the
multi-platform image index and the Linux `amd64` platform manifest in support
evidence, and reject tag-only image references.

The selected issue-68 packaging identity is:

- PostgreSQL `17.6`;
- image index
  `sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929`;
- Linux `amd64` manifest
  `sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409`;
- service identity `provision-lab-postgresql.service`;
- container identity `provision-lab-postgresql`;
- Environment account `provision-lab`;
- initial generation `postgresql-17-6-b86568d3e0fe`; and
- generation data path
  `/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-17-6-b86568d3e0fe/data`.

The managed path deliberately avoids unrecorded mutation of a shared
host-installed PostgreSQL package. Host-installed PostgreSQL may still be
observed and bound as an External Component, but this decision does not adopt
or manage it.

## Consequences

- Host inspection now reports a first-class `database` capability block
  separate from the asynchronous Queue/Worker/Schedule block.
- Later Database Plans can distinguish the exact qualified PostgreSQL
  packaging identity from broad PostgreSQL assumptions.
- This packaging proof advertises no executable Store Transition, lossless
  cutover, or Store Rollback Guarantee. Those remain future Database
  transition work.
- The proof qualifies service start, real SQL connectivity, systemd encrypted
  credential delivery by non-secret reference, owned generation data paths, and
  durable reboot behavior only for the listed single-host topology. It does not
  assert encrypted backing media.
- It does not qualify production Database lifecycle operations, side-by-side
  Store Transitions, backup, restore, host-loss recovery, multi-node
  availability, RDS, external PostgreSQL, or other PostgreSQL versions.

## Rejected alternative

The Ubuntu-native PostgreSQL package is not selected for the curated managed
implementation. It is host-wide package state, couples all Environments on the
Host to one package transaction, and does not provide an Environment-scoped
immutable runtime identity or generation-specific data directory by default.

## Acceptance evidence

The non-secret approval record is
[`2026-09-28-postgresql-packaging-approval.json`](../evidence/2026-09-28-postgresql-packaging-approval.json).
The issue-specific proof harness is
[`prove-postgresql-quadlet-host.sh`](../../scripts/prove-postgresql-quadlet-host.sh),
driven by
[`run-postgresql-quadlet-acceptance.sh`](../../scripts/run-postgresql-quadlet-acceptance.sh).

The proof is destructive and is scoped to a disposable Ubuntu 26.04 Host. It
restores the clean VM snapshot, installs the qualified Podman package, prepares
the Environment account, starts PostgreSQL from the approved manifest, proves
SQL connectivity and a durable row, reboots the VM, verifies the same row after
restart, copies evidence, and restores the clean snapshot again.

The live proof passed on 2026-09-28. The support note is
[`2026-09-28-postgresql-packaging-comparison.md`](../evidence/2026-09-28-postgresql-packaging-comparison.md),
and the support matrix records the exact Host/runtime combination and
exclusions.
