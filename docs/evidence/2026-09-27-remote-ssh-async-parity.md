# Remote SSH asynchronous parity evidence — 2026-09-27

Issue #50 qualified the complete asynchronous Host tracer through both local
and strict SSH management paths against clean restores of the disposable
`provision-acceptance` Ubuntu VM.

The top-level harness was:

```sh
./scripts/run-remote-ssh-async-acceptance.sh
```

It first ran the direct-local asynchronous matrix and then the matching remote
SSH matrix. The VM was restored to the clean snapshot before each scenario and
again after the definitive run.

## Exact versions

- source identity recorded by evidence:
  `506bd3e03f2f91f207b1cbddc45c8173fbb7b697-dirty-3bde0f05c37df4cd1dc3367f1c3d4f64fff7c2560b00b60c3942bd1782ffd7ee`
- remote controller: macOS Darwin `24.6.0`, `arm64`
- remote controller binary:
  `sha256:5f6b4a090d49bf810ca0ecdc18f2700dcecbf510a10178894e90e1a9c1828bc2`
- direct-local VM binary:
  `sha256:6fde40576b06e61c2131afc5e137d1254f976063287a0e4f986899a1631be5c7`
- restricted Linux host executor and pinned schedule applet:
  `sha256:0b62d993842142e894acc52c5b7810cb4f5cb91c958ac51d255c2379d77a9607`
- Host Target: Ubuntu Server `26.04`, `x86_64`, systemd
  `259 (259.5-0ubuntu3.4)`
- SSH server: `OpenSSH_10.2p1 Ubuntu-2ubuntu3.6, OpenSSL 3.5.5 27 Jan 2026`
- SSH host-key fingerprint:
  `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs`
- Podman: `5.7.0+ds2-3build1`
- RabbitMQ: `4.3.6`
- RabbitMQ image manifest:
  `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91`
- schedule ledger schema: `provision.dev/schedule-ledger/v1alpha1`

## What passed

Both direct-local and remote SSH ran the same configuration, planner,
approval, operation order, application artifacts, recovery harnesses, and final
accounting checks. The remote path used strict host-key checking and
passwordless SSH; the controller retained the State Backend, authority key,
approvals, and secrets.

The direct-local evidence is under
`work/issue-50-live/evidence/direct-local`. The remote SSH evidence is under
`work/issue-50-live/evidence/remote-ssh`. The parity summary is
`work/issue-50-live/evidence/parity-summary.json`.

Two scenarios passed for each transport:

| Scenario | Direct-local accounting | Remote SSH accounting | Result |
| --- | --- | --- | --- |
| Worker rollback | 2 message observations, 1 occurrence observation, 1 Task Invocation identity | 2 message observations, 1 occurrence observation, 1 Task Invocation identity | matched host OS/version/arch, systemd, executor digest, RabbitMQ manifest, schedule applet digest, message and occurrence behavior, active/retained Generation accounting, owned-resource inventory, and artifact/runtime versions |
| Complete async tracer | 3 message observations, 2 occurrence observations, 2 Task Invocation identities | 3 message observations, 2 occurrence observations, 2 Task Invocation identities | matched host OS/version/arch, systemd, executor digest, RabbitMQ manifest, schedule applet digest, message and occurrence behavior, active/retained Generation accounting, owned-resource inventory, and artifact/runtime versions |

The remote matrix injected controller/transport loss around:

- Queue preparation, before Host dispatch and after Host completion;
- Task delivery verification, after Host completion;
- Worker fence, drain, activation, active verification, rollback, and
  retention boundaries; and
- Schedule handoff and Schedule verification.

The complete remote scenario also rebooted the Host Target and proved the
stable timer resumed without depending on a resident Provision controller.
The parity summary records the exact active Queue, Worker, Task, and Schedule
Generations, retained Worker Generation, 15 owned Queue resources, six artifact
or runtime-version digests, and the distinct Task Invocation identities observed
for each transport.

## Boundaries

This evidence qualifies one disposable Ubuntu `26.04` `x86_64` VM running
systemd, rootless Podman/Quadlet, one managed RabbitMQ Queue, one systemd
Worker, one systemd Task, and one stable Schedule. It does not qualify Queue
generation migration, exactly-once delivery or processing, autoscaling, other
Host OS or dependency versions, AWS/ECS/Lambda renderers, realtime servers,
databases, caches, store transitions, or Host/RabbitMQ data-loss recovery.

The run discovered and fixed a reboot-ordering problem: retained Worker units
and active Worker units could start before RabbitMQ and hit systemd start-rate
limits after reboot. The executor now orders generated Task and Worker units
after the managed RabbitMQ service and disables retained previous Worker units
at retention time while keeping their restart material intact.

The final history check was also narrowed so it rejects identity rewrites while
allowing independent schedule work to advance from `recorded`/`pending` to
`succeeded`/`succeeded` during a read-only status observation.
