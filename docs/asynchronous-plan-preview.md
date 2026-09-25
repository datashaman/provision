# Asynchronous Host Plan preview

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
gh release download v0.1.0 \
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

This increment defines validation and planning only. The asynchronous operation
kinds are not enabled in the restricted Host executor yet. Queue provisioning,
Worker and Task rendering, schedule runtime installation, execution, resume,
and live acceptance are separate issues in the asynchronous tracer.
