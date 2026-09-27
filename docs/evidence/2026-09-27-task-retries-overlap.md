# Task Invocation retry and overlap evidence

Issue [#45](https://github.com/datashaman/provision/issues/45) extends the
host Schedule runtime. The occurrence ledger now records each Invocation's
selected Task unit, Revision and configuration digest, referenced Queue,
retry and overlap policy, ordered attempts, launch identity, and outcomes.

## Deterministic runtime checks

`go test -race ./...` and `go vet ./...` passed on 2026-09-27.
The runtime tests call the same `run --environment lab --schedule every-minute`
parser and execution path as the installed applet, with a controlled clock and
systemd boundary. They cover failed and timed-out attempts followed by success,
retry delay, exact Generation and stable identity, ambiguous delivery, long
Task completion, allowed and forbidden overlap, and a retry still running at
the next due time. A concurrent two-connection SQLite test verifies that only
one attempt can claim a forbidden overlap.

## Disposable VM smoke test

The VM `provision-acceptance.local` was restored from its `clean` snapshot,
bootstrapped with executor digest
`sha256:658fc3ac7994d91e83baca6f3da8d0c4ed2ce871cefd02e9424888f30be7952c`,
and exercised with `scripts/test-first-scheduled-message-host.sh`. It passed
all six stages. Evidence is under `/tmp/provision-issue45-evidence-final` on
the controller. The verified occurrence was
`occ-6c5a40c3db910e630cd18c35`; its Task Invocation
`inv-6c5a40c3db910e630cd18c35` succeeded in one attempt, the message was
publisher-confirmed and Worker-acknowledged, and status declared
`at-least-once` delivery.

The observed stack was Ubuntu 26.04, systemd 259 (259.5-0ubuntu3.4),
Podman 5.7.0+ds2-3build1, and RabbitMQ 4.3.6. This smoke test proves the
existing complete asynchronous path after the Task unit change. Live retry,
timeout, and overlap fault injection remain part of the later direct-local
asynchronous failure matrix (#49); the deterministic runtime tests above
exercise those policies in this PR.
