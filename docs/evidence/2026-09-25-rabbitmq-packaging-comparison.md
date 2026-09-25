# RabbitMQ Host packaging comparison — 2026-09-25

**Decision state: approved and live-qualified. No production Queue executor
operation was enabled.**

This evidence for issue #36 compares native Host packaging with a digest-pinned
OCI/Quadlet deployment on the clean `provision-acceptance` VM. The comparison
was gathered read-only. After the operator approved the recommendation, the
selected path was installed and exercised on a fresh restore of that VM,
reboot-qualified, recorded in the support matrix, and removed by restoring the
clean snapshot again.

## Reproduce the observation

The committed probe accepts only an exact OCI digest and emits versioned JSON:

```sh
./scripts/inspect-rabbitmq-packaging.sh \
  --target marlinf@provision-acceptance.local \
  --environment lab \
  --oci-image docker.io/library/rabbitmq@sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91
```

The comparison probe uses strict, non-interactive SSH and read-only operating-system,
package-index, account, systemd, and filesystem observations. It contains no
package installation, image pull, account creation, unit start, or service
enablement path. Its output always says `decisionStatus:
awaiting-human-approval` and `mutatingOperationsEnabled: false`. Those fields
describe this comparison probe, not the later, explicitly approved acceptance
proof.

The RabbitMQ official-image index was resolved independently from the
controller on the same date. `rabbitmq:4.3.6` resolved to multi-platform index
`sha256:d0bffe70e755f348625415f32b0a090662e5f06b3ba3f82a4c7aaa18621b1279`;
the Linux `amd64` manifest proposed for this VM is
`sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`.
The manifest identifies RabbitMQ `4.3.6`, Ubuntu 24.04 base image
`sha256:496754492fb28b4d3049432f2ca787449331e23fb14f0dd3fffea86bf5a93eb4`,
and upstream image-source revision
`76efb370838e36e4b120e252e75cf0dac70a7d55`.

## Clean-host observation

| Property | Observed value |
| --- | --- |
| Host | Ubuntu Server 26.04, Linux `7.0.0-34-generic`, `x86_64` |
| systemd | `259 (259.5-0ubuntu3.4)` |
| cgroup | v2 available |
| RabbitMQ installed | no |
| Native candidate | `rabbitmq-server 4.0.5-10ubuntu5`, Ubuntu `resolute/main` |
| Native package bytes | `sha256:facf754e045a705aca30b82b7728ac7b3f123d2b7f7838575b246fe0bd2ad05e` |
| Podman installed | no |
| Podman candidate | `5.7.0+ds2-3build1`, Ubuntu `resolute/universe` |
| Podman package bytes | `sha256:bc8fbe3c31f0904550a9e6cb32363643fd34ad9b78553fda233b50caef2e11bd` |
| `provision-lab` account | absent on the clean pre-bootstrap image |
| Queue topology under comparison | one RabbitMQ node, one quorum member |
| Host-failure tolerance | zero for either packaging option |

The proposed topology deliberately separates durability from availability. A
single-member quorum Queue may persist confirmed messages across a healthy
broker or Host restart, but it cannot tolerate loss of its only node or Host.
RabbitMQ documents three members as the practical minimum for tolerating one
node failure and states that publisher-confirmed messages rely on a majority
of Queue members remaining available.

## Criteria comparison

This table records what was known at the approval gate; the selected option's
previously unproved properties are resolved by the live result below.

| Criterion | Native `rabbitmq-server` | Digest-pinned rootless OCI/Quadlet |
| --- | --- | --- |
| Immutable product identity | Exact package version and `.deb` digest are visible, but a safe Plan must also bind the complete Erlang dependency closure, repositories, signing identities, and package holds. | Exact index and target-platform manifest digests bind RabbitMQ and its user-space dependency filesystem. The Host still separately binds the Podman package and capability evidence. |
| Unattended restart | Conventional system service; expected to be simple, but not installed or reboot-proved in this phase. | Quadlet user unit plus lingering is designed for boot persistence. Podman requires cgroup v2, which is observed. Account, linger, unit generation, and reboot remain to be proved live. |
| Privilege isolation | Broker runs as the unprivileged `rabbitmq` service identity, but repository, package, configuration, and service lifecycle are root-managed and Host-wide. | Broker and container runtime run as the dedicated `provision-lab` identity. Root is limited to bootstrap, root-owned definitions, data-path preparation, and the restricted executor. |
| Owned paths | Conventional Host-wide `/etc/rabbitmq`, `/var/lib/rabbitmq`, and `/var/log/rabbitmq`; shared package ownership complicates multiple Environments. | Environment-scoped Quadlet definition, rootless storage, and `/var/lib/provision/environments/lab/services/rabbitmq`; exact paths and ownership remain live acceptance assertions. |
| Credential delivery | A system service can use systemd credentials through a root-owned drop-in; RabbitMQ consumption and non-disclosure still need live proof. | The user unit can use encrypted systemd credentials and mount only the per-unit credential files into the container; generation and container visibility still need live proof. |
| Capability observation | RabbitMQ CLI tools run directly as the service identity. Product version, node identity, enabled features, vhost, Queue type/member count, confirms, acknowledgements, alarms, and health can be observed. | The same RabbitMQ observations are executed through a digest-bound container identity; Podman adds image/config/state and generated-unit observations. At the approval gate, neither option had been live-proved. |
| Upgrade and rollback | A package transaction mutates the Host-wide broker and Erlang runtime in place. Downgrade may be unsafe after a data or feature transition. | A new image digest is an explicit immutable runtime candidate, but the durable Queue data is shared. Image rollback is still forbidden unless RabbitMQ data compatibility is proved. |
| Support evidence at approval gate | Ubuntu offers 4.0.5, but RabbitMQ's installation guide warns that standard Debian/Ubuntu packages are commonly behind or out of community support; its current supported-distribution list does not yet include Ubuntu 26.04. | RabbitMQ 4.3.6 is the current official release and an official image exists for the target architecture. Provision had no qualified row until the later install, reboot, Queue semantics, and inventory tests passed. |

## Approved decision

The operator approved the digest-pinned rootless OCI/Quadlet option for the
managed Host implementation. It best preserves Environment-scoped ownership,
immutable runtime identity, independent version selection, and auditable
upgrades. It also follows the existing rootless Podman/Quadlet architecture
decision.

Approval authorized only the issue-specific acceptance proof on a freshly
restored VM:

1. install and observe the exact Podman/Quadlet bootstrap dependency;
2. prepare the dedicated Environment account, subordinate IDs, linger, and
   narrowly owned state paths;
3. pull and verify only the approved platform manifest;
4. deliver non-secret test credentials via encrypted systemd credentials;
5. start the environment-scoped broker and prove exact RabbitMQ/node/Queue
   identity, publisher confirms, acknowledgements, and one-member topology;
6. reboot the VM and prove the same identity, data, unit, and Queue health;
7. capture exact inventory, restore the VM, update the support evidence, and
   change the ADR status to Accepted only if every assertion passed.

All seven steps passed. The native option was rejected for this curated
implementation because one Environment could not independently own its
host-wide RabbitMQ/Erlang package and upgrade transaction.

## Live qualification result

The destructive proof used a purpose-built black-box AMQP client and a narrow
Host harness; neither is a production Queue executor. The VM began from its
stopped `clean` snapshot. The prepare phase installed exact Podman version
`5.7.0+ds2-3build1`, created only Environment identity `provision-lab`, pulled
only the approved RabbitMQ platform manifest, and started rootless unit
`provision-lab-rabbitmq.service`.

The broker reported RabbitMQ `4.3.6`, node
`rabbit@provision-lab-rabbitmq`, and one durable quorum Queue named
`provision-issue36`. The client proved a persistent publish with a publisher
confirm and a separately manual-acknowledged delivery. It then left confirmed
message `issue36-reboot-survival` queued, rebooted the Host, and consumed and
acknowledged that exact message ID after the rootless unit restarted healthy.

The proof also asserted that:

- the OCI image and running container both bound platform manifest
  `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`
  and image configuration
  `sha256:5e82332c48dd5c5fe3299ab7ed6550d860801cf9a0330d262d57cadee1ac7899`;
- the Environment account owned exactly one image and one container, with one
  root-owned Quadlet definition and no native `rabbitmq-server` package;
- the systemd credential was encrypted for the Environment user, mounted
  read-only, copied only into a container tmpfs for the RabbitMQ service UID,
  and absent as plaintext from durable service storage;
- the package inventory did not change across reboot; and
- the one-broker, one-voter topology has zero Host-failure tolerance despite
  surviving an orderly restart.

The generated acceptance client was Linux `x86_64` binary
`sha256:9dd6ae1360318ed4b7b119486d29181b9f84ebaea3b9773e929ed213a6aefcfe`.
The complete non-secret result is the versioned
[`provision.dev/rabbitmq-packaging-qualification/v1alpha1` observation](2026-09-25-rabbitmq-packaging-qualification.json).
Raw evidence was copied to the ignored local work area before the VM was
restored. A clean-host check afterward confirmed that Podman, the
`provision-lab` account, and issue evidence were absent again.

The selected image identity does not make persisted RabbitMQ data safe to
downgrade. Upgrades still require an explicit data-compatibility decision and
their own support evidence. This proof does not enable a production executor
operation.

## Primary references

- [RabbitMQ installation on Debian and Ubuntu](https://www.rabbitmq.com/docs/install-debian) recommends Team RabbitMQ repositories over standard distribution packages, explains exact package pinning, and records the native `rabbitmq` service identity.
- [RabbitMQ quorum queues](https://www.rabbitmq.com/docs/quorum-queues) defines publisher-confirm safety and the practical three-member minimum for one-node failure tolerance.
- [Podman Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html) documents rootless unit locations, digest image references, systemd generation, and the cgroup-v2 prerequisite.
- [systemd service credentials](https://man7.org/linux/man-pages/man5/systemd.exec.5.html) documents per-unit read-only credential files and the user-manager credential boundary.
- [RabbitMQ official OCI image](https://hub.docker.com/_/rabbitmq/) identifies the maintained image and published version families.
