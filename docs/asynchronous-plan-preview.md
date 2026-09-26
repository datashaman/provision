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

This first tracer does not implement Worker replacement, Schedule handoff
between revisions, retry/redelivery policy, overlap behavior, missed-run
catch-up, crash-boundary recovery, or Queue-generation replacement. Those
remain explicit later transitions rather than implied guarantees.
