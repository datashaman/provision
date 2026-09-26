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
| Provision binary | `sha256:51b933ac55c4c86de10eb78f6bf6e983e83bf5ec22de71bcd0999745d6237e20` |
| Restricted executor and pinned schedule applet | Linux `x86_64`, `sha256:bf891bd83aa47eeab8cbc3916b88a546e6ab8eeb5c0b09acb1671f51c4ad8ed8` |
| Host Target | Ubuntu Server `26.04`, `x86_64`; systemd `259 (259.5-0ubuntu3.4)`; OpenSSH `10.2p1 Ubuntu-2ubuntu3.6` |
| Management transport | strict SSH host key `SHA256:3jl37pF+Utxxbic8EUHRQ5qshhP6SdxOX6SnXHjg+Qs` |
| Authorization | authority key ID `sha256:5ded445e242a8c5aca90da032b444d10ec94935b07a29f81613ddc3ba0a2be95` |
| Queue runtime | Podman `5.7.0+ds2-3build1`; RabbitMQ `4.3.6`; image manifest `sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91` |
| Worker Artifact | `provision-example-async` v0.1.0, `sha256:4b6e79444cd9032facb5e027cafb7dca328d5f33eb334ccc3e83e70a30ce6e4a` |
| Task Artifact | `provision-example-async` v0.1.0, `sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5` |
| Plan | `sha256:1a66eade151bd4ca9b49621a110108b17a234c3c75bcc6c6f5b23f8edc90677f`; thirteen signed operations |

## Proven path

The Plan prepared the exact managed RabbitMQ Queue, staged both released
Artifacts, and installed distinct immutable Task and Worker generations. The
Task used a hardened generation-specific template unit. The Worker started with
intake closed, proved its process, Revision identity, Queue connection, and
closed admission state, then opened intake and recorded the exact active
generation.

The executor installed its exact bytes as
`/usr/local/libexec/provision-runtime-schedule`. The stable
`provision-lab-every-minute.timer` invoked that nonresident applet without a
running management CLI. The applet committed occurrence
`occ-1f767fc6dbe82fe77417eaba` and Task Invocation
`inv-1f767fc6dbe82fe77417eaba` before starting the exact Task instance.

The invocation record bound Application Revision
`provision-example-async-v1`, Environment Configuration digest
`sha256:20fcf7ef7dcdda320b488f5d4081a98201007c6781c5e378fc73c00eca5ca8a2`,
Task generation `provision-example-async-v1-ce1dc7e13900`, Schedule fencing
token `12`, one attempt, start and completion times, successful outcome, and a
non-secret evidence path.

That Task received publisher confirmation for message
`msg-6f9a6c0fb7fbc4bd2689e7e36f1df3a275ead1feb0837ec607407eb65ec5ba9c`.
The active Worker recorded processing and its manual acknowledgement decision
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
| `plan.json` | `7e6ee93f19fd1aadfb94765935fca035c397b880e4965e438fa9f2852e64dff4` |
| `approval.json` | `3ce0b4765c9634165dfa4c54d0bcad57cbe000305c32e36cfe5f25d9adb4406a` |
| `op-14.json` | `f37d415b26d571033c382119e53b4aac294a538bc187ea148474070301b9b04d` |
| `deployment-status.json` | `7f04433af7bd43010651ffe68731d9646464560dc6cb2d0c9d82752cc60732e5` |
| `bootstrap-after.json` | `819768c321d160fae67a7a472dc4f62e7130a5bacb96d0fdf94457c064e0bba5` |

This evidence qualifies only the initial single-Host path. It does not qualify
Worker replacement, Schedule handoff between revisions, retry/redelivery,
overlap, missed-run catch-up, crash-boundary recovery, Queue-generation
replacement, multi-node availability, EC2, ECS, Lambda, or SQS.
