# Schedule civil-time and missed-run evidence

Issue [#46](https://github.com/datashaman/provision/issues/46) moves
Schedule timing decisions into the pinned host Schedule applet rather than
delegating Schedule meaning to systemd calendar syntax.

## Deterministic policy checks

`internal/scheduler` now evaluates five-field cron expressions against a
declared timezone using wall-clock labels. UTC remains valid as an explicit
timezone, and named zones are preserved in the installed Schedule record and
reported status.

The policy tests cover:

- a daylight-saving gap for `Europe/Berlin` where `02:30` is recorded as
  `skipped-dst-gap` instead of being silently shifted to `03:30`;
- a daylight-saving fold for `America/New_York` where one wall-clock label
  creates one occurrence rather than a duplicate;
- `missedRun: skip`, which records missed occurrence dispositions without
  launching Task attempts;
- `missedRun: bounded-catch-up`, which records older missed occurrences as
  skipped and emits no more than the configured catch-up bound.

## Runtime checks

The Schedule runtime tests exercise the same `run --environment lab --schedule
every-minute` applet entrypoint with a controlled clock and systemd boundary.
They prove that:

- skipped missed occurrences do not launch Task units;
- bounded catch-up preserves the same Task Invocation identity across retry;
- retry delay and forbidden-overlap policy still compose after catch-up
  decisions;
- no new due occurrence still reconciles pending retries instead of returning
  early.

## Disposable VM acceptance

`scripts/test-schedule-policy-host.sh` passed on
`provision-acceptance.local` on 2026-09-27. The VM used the current
restricted executor digest
`sha256:9d8554118a8c6ba2a2594fb7ba424de61335b814123cc8464cc3d0d95`.

The first stage reused the complete asynchronous Host acceptance path and
proved a normal occurrence under `/tmp/provision-issue46-evidence/normal`.
The policy stage then stopped the stable systemd timer, drove the installed
digest-pinned applet directly on the disposable Host, and captured evidence
under `/tmp/provision-issue46-evidence-policy-final`.

The live policy evidence recorded:

- normal wall-clock occurrence `2026-09-27T07:14`;
- missed-run skip for `2026-09-27T07:15`, returning
  `skipped-missed` without a Task attempt;
- bounded catch-up with `maxOccurrences: 2`, where older missed occurrences
  remained skipped and only `2026-09-27T07:17` and `2026-09-27T07:18`
  launched Task attempts;
- structured Host status reporting `wallDueAt`, occurrence disposition,
  Task Invocation outcome, and attempt counts for the same records.

`go test ./...`, `go vet ./...`, and `go test -race ./...` passed on
2026-09-27.
