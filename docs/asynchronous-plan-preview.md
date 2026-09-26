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

For a Worker-only replacement, the Planner keeps the existing Queue, Task, and
Schedule identities and emits one explicit chain: Queue verification, Artifact
staging, immutable candidate installation, gated start, candidate verification,
old-Worker intake fence, bounded in-flight drain or release, candidate
activation, active verification, and previous-generation retention. Every
operation depends on the preceding transition; the approved input pins both
Worker generations, the unchanged Queue generation, the drain bound, and the
rollback window. Post-drain activation, verification, and retention also carry
the exact approved drain-operation digest.

The restricted executor requires the root-owned gate file and Worker runtime
state to agree before it reports intake closed. Closing the old gate stops new
claims but permits its one already accepted delivery to finish. The declared
maximum includes a deterministic release reserve: normal completion is allowed
until the recorded completion deadline, then the executor starts the bounded
unit stop so any unfinished delivery is negatively acknowledged and requeued by
the final drain deadline. The durable drain record names the same stable message
identity and records release start and completion. Candidate intake cannot open
until the exact old unit is fenced,
stopped, free of in-flight work, and backed by that drain record. After active
verification, the old immutable artifact, generation record, unit, and closed
gate remain restartable through the recorded rollback deadline.

Active verification is message-level rather than process-only. Before the
candidate becomes authoritative, the executor records a Plan- and
operation-bound verification intent, publishes one persistent stable-identity
message with broker confirmation, and requires matching processing and manual
acknowledgement evidence from the candidate. Only then does it commit the exact
active and previous Worker identities. The Queue generation is re-observed
before this decision.

If the candidate loses its verified active state before acknowledging that
message, the same operation first closes and proves its intake fence, stops and
disables its unit, re-observes the unchanged Queue, starts the retained previous
generation gated, and only then reopens its intake. Rollback succeeds only when
the restored generation processes and acknowledges the same stable message
identity. A broker redelivery or the Worker's duplicate ledger remains an
at-least-once outcome; it is never labelled exactly once. An unprovable fence,
Queue identity, retained generation, message disposition, or durable authority
record produces an explicit uncertain result with operator recovery guidance.

The drain record is written before the executor waits. It binds the Plan,
operation, two Worker generations, Queue generation, stable in-flight message
identity, start, completion deadline, final deadline, optional release start,
and completion. A successful result proves settlement no later than the final
deadline and requires exactly one durable acknowledgement or requeue outcome.
A resumed attempt reuses both deadlines; if an interruption prevents proof that
settlement stayed inside them, the operation does not become success. Stale
Plans, changed Queues, missing closed gates, and historical-but-no-longer-active
Worker records fail closed.

If candidate verification fails, the candidate remains closed and separately
actionable while the existing Worker continues handling normal messages. The
checked-in
[`scripts/test-rejected-worker-candidate-host.sh`](../scripts/test-rejected-worker-candidate-host.sh)
harness and its [live evidence](evidence/2026-09-26-rejected-worker-candidate.md)
qualify that rejection path.

The successful path is exercised twice by
[`scripts/test-worker-intake-handoff-host.sh`](../scripts/test-worker-intake-handoff-host.sh):
once with the old Worker completing its held delivery inside the bound, and once
with the deadline releasing the same message identity for the candidate. Its
live evidence is recorded in the
[Worker intake handoff evidence](evidence/2026-09-26-worker-intake-handoff.md).

The message-level active check and automatic supported rollback are exercised
by
[`scripts/test-worker-active-rollback-host.sh`](../scripts/test-worker-active-rollback-host.sh):
one clean-snapshot run commits a healthy candidate, while another stops the
candidate after activation and proves restoration of the previous Worker. Its
live evidence is recorded in the
[Worker active-verification evidence](evidence/2026-09-26-worker-active-rollback.md).

Every externally visible Worker handoff operation is resumable under a new,
higher fencing token. Resume first re-observes the exact Queue, both Worker
generations, systemd units, root-owned gates, durable drain or retention record,
and active authority. An already completed fence, drain/release, activation,
verification, rollback, or retention mutation is committed from that evidence
without dispatching it again. Publisher-confirmed verification resumes from the
same stable message and recorded rollback checkpoints; a publish whose
confirmation was not durably recorded is ambiguous and pauses with the
candidate fenced rather than guessing. A late lower-fence result is retained as
non-authoritative diagnostic evidence and cannot change the journal or active
Worker. The checked-in
[`scripts/test-worker-handoff-recovery-host.sh`](../scripts/test-worker-handoff-recovery-host.sh)
harness injects controller death before dispatch and after Host completion at
each boundary. Its qualified run is recorded in the
[Worker handoff recovery evidence](evidence/2026-09-26-worker-handoff-recovery.md).

This tracer does not yet implement Schedule handoff between revisions, general
retry/dead-letter policy acceptance, overlap behavior, missed-run catch-up,
retention expiry cleanup, Queue-generation replacement, or recovery after loss
of the Host or RabbitMQ data. Those remain explicit later
transitions rather than implied guarantees.
