# First scheduled-message evidence — 2026-09-26

Issue #40 was exercised from a freshly restored `provision-acceptance` VM
snapshot with the checked-in
`scripts/test-first-scheduled-message-host.sh` harness. The six-stage run ended
with `first scheduled message acceptance passed` and retained its structured
controller evidence under `/tmp/provision-issue40-evidence`.

## Tested combination

| Property | Observed value |
| --- | --- |
| Controller | macOS `15.7.7`, Darwin `24.6.0`, `arm64` |
| Provision binary | `sha256:281e5aaecb93d8134d2b408866f448a1294d9392a5e25455cd13b714d9484128` |
| Restricted executor and pinned schedule applet | Linux `x86_64`, `sha256:a0392678ef0636e577e2a850e0491b49160b0ce17bd834e4db96fa7f8be46e87` |
| Host Target | Ubuntu Server `26.04`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Management transport | strict SSH host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Authorization | authority key ID `sha256:5ded445e242a8c5aca90da032b444d10ec94935b07a29f81613ddc3ba0a2be95` |
| Queue runtime | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` |
| Worker Artifact | `provision-example-async` v0.1.1, `sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40` |
| Task Artifact | `provision-example-async` v0.1.1, `sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5` |
| Plan | `sha256:f1e3e2fb07c42a5495dc27d1ec08117c462ac5118f56309232c616891b099171`; thirteen signed operations |

## Proven path

The Plan prepared the exact managed RabbitMQ Queue, staged both released
Artifacts, and installed distinct immutable Task and Worker generations. The
Task used a hardened generation-specific template unit. The Worker started with
intake closed, proved its process, Revision identity, Queue connection, and
closed admission state, then opened intake and recorded the exact active
generation.

The executor installed its exact bytes as
`/var/lib/provision/environments/lab/runtime/provision-runtime-schedule`. The
stable `provision-lab-every-minute.timer` invoked that nonresident applet without a
running management CLI. The applet committed occurrence
`occ-877fb257e037f97a9df1fa13` and Task Invocation
`inv-877fb257e037f97a9df1fa13` before starting the exact Task instance.

The invocation record bound Application Revision
`provision-example-async-v1`, Environment Configuration digest
`sha256:e4a04e46e32ce7efa9fcbe72544ef390af2f833a2f9917d910da3d475490467b`,
Task generation `provision-example-async-v1-ce1dc7e13900`, Schedule fencing
token `12`, one attempt, start and completion times, successful outcome, and a
non-secret evidence path.

That Task received publisher confirmation for message
`msg-40336c542c0dc2b6f535f0cb956913642ca9737fbf3a2653a3f341e40454b153`.
The active Worker recorded processing and broker-confirmed manual acknowledgement
for the same message identity. The final restricted inspection reported the
Queue, active Worker, active Task, Schedule, occurrence, and Task Invocation as
separate structured entities with no findings.

## Credential and diagnostic boundary

RabbitMQ's rootless user service receives a user-manager-scoped encrypted
configuration credential. The system-manager Task and Worker units receive a
separate system-scoped encrypted broker URL credential. No plaintext broker
credential appeared in the Configuration, Plan, approval, journal, operation
results, status, or retained harness files. systemd warned that the disposable
VM's credentials host key is stored on unencrypted media, so this run proves
encrypted delivery and plaintext non-disclosure, not resistance to full host
or disk compromise.

Failed host outcomes now retain the operation ID, operation kind, structured
executor reason, and safe systemd state. Worker startup diagnostics include
active/sub states, unit result, exit code/status, failure stage, and restart
count without copying arbitrary application logs or credential values.

## Evidence identities

| File | SHA-256 |
| --- | --- |
| `plan.json` | `b126baa385bc9c48c4f5386cf4eec0c075eddb771ad05a1ca475258580240b0e` |
| `approval.json` | `cbc45e5e84baf4b3f9a83d0d65346f1de2f33a29b4d81897f0f6c57846d7f6dd` |
| `op-14.json` | `2257e778d6ae072a888ae8199bd8fc1da178871f79a9a781bcb13e61f7a4d5db` |
| `deployment-status.json` | `73c55d1c9f3ebf22845d0936426e27ce61be08b774192ce9fcd556abe9eec410` |
| `bootstrap-after.json` | `e1c461c7306308202ee154899a126aaba27778536a660c67be3fcf44e6072aa4` |

This evidence qualifies only the initial single-Host path. It does not qualify
Worker replacement, Schedule handoff between revisions, retry/redelivery,
overlap, missed-run catch-up, crash-boundary recovery, Queue-generation
replacement, multi-node availability, EC2, ECS, Lambda, or SQS.
