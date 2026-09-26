# Asynchronous Host execution

Provision's asynchronous Host example describes four distinct component roles:

- one managed RabbitMQ quorum Queue;
- one persistent, gated systemd Worker that consumes that Queue;
- one generation-specific systemd Task; and
- one Schedule that invokes the Task through a stable timer and pinned runtime applet.

The example uses the independently released, framework-neutral
[`datashaman/provision-example-async`](https://github.com/datashaman/provision-example-async)
Worker and Task artifacts. Validate both downloaded archives by naming the
component each file satisfies:

```sh
gh release download v0.1.1 \
  --repo datashaman/provision-example-async \
  --pattern 'provision-example-async-*-linux-amd64.tar.gz' \
  --dir /tmp/provision-example-async

go run ./cmd/provision config validate \
  --file examples/host-async/root.yaml \
  --artifact-file consumer=/tmp/provision-example-async/provision-example-async-worker-linux-amd64.tar.gz \
  --artifact-file publish=/tmp/provision-example-async/provision-example-async-task-linux-amd64.tar.gz
```

`plan preview` performs only the configured Host Target's restricted inspection.
It never installs RabbitMQ, starts a Worker, invokes a Task, or changes a timer:

```sh
go run ./cmd/provision plan preview --file examples/host-async/root.yaml
```

The Plan binds the exact RabbitMQ image index and Linux `amd64` manifest, both
application Artifact digests, the Queue Secret Reference, the pinned schedule
applet and ledger format, Host and Queue observations, current generations,
and role-specific recovery consequences. Resolved broker credentials are never
part of configuration, errors, or Plan data.

After persisting and approving that exact Plan, `deployment execute` can apply
the initial asynchronous graph through the same signed, fenced operation seam
as HTTP deployments. The restricted executor:

- prepares the exact managed Queue and its separate user-manager and
  system-manager encrypted credentials;
- installs the Worker and Task as immutable Artifact-bound generations;
- starts and verifies the Worker with intake gated before opening it;
- installs the executor's exact bytes as a pinned, nonresident schedule applet;
- hands a stable timer to the exact Task generation; and
- verifies one occurrence, Task Invocation, publisher-confirmed message, and
  Worker acknowledgement by stable identities.

The schedule applet commits the due occurrence and stable Task Invocation to a
SQLite ledger before starting the Task instance. Its record binds the
Application Revision, Environment Configuration digest, Task generation,
fencing token, attempt, timing, outcome, and non-secret evidence path. Status
reports the Queue, active Worker, active Task, Schedule, occurrence, and Task
Invocation separately.

The checked-in black-box harness is
[`scripts/test-first-scheduled-message-host.sh`](../scripts/test-first-scheduled-message-host.sh).
Its qualified live run is recorded in the
[first scheduled-message evidence](evidence/2026-09-26-first-scheduled-message.md).

For a Worker-only candidate, the Planner keeps the existing Queue, Task, and
Schedule identities and emits only Queue verification, Artifact staging,
immutable candidate installation, gated start, and candidate verification. It
does not authorize the still-unimplemented intake fence, old-Worker drain,
candidate activation, active verification, or retention operations. The
restricted executor requires the root-owned gate file and Worker runtime state
to agree before it reports intake closed, and it reports process identity,
Queue connection, Revision identity, complete per-message Worker event history,
and safe systemd diagnostics. If verification fails, the candidate remains
closed and separately actionable while the existing Worker continues handling
normal messages. The checked-in
[`scripts/test-rejected-worker-candidate-host.sh`](../scripts/test-rejected-worker-candidate-host.sh)
harness and its [live evidence](evidence/2026-09-26-rejected-worker-candidate.md)
qualify that rejection path.

This tracer does not yet implement a successful Worker intake handoff, old
Worker drain and retention, Schedule handoff between revisions,
retry/redelivery policy, overlap behavior, missed-run catch-up, crash-boundary
recovery, or Queue-generation replacement. Those remain explicit later
transitions rather than implied guarantees.
