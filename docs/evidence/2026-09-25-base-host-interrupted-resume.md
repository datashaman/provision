# Base host interrupted Deployment resume evidence — 2026-09-25

Issue #11 was exercised from PR #26 on a freshly reset, disposable `base.local` host. The host ran Ubuntu Server 26.04 at `192.168.101.109` during the run.

## Bootstrapped authority

Bootstrap inspection reported `ready: true` with no findings:

- SSH ED25519 host fingerprint: `SHA256:lnAjSgeyi3gxLq2vh6BrOLmiJwMI7CKH+o9DZfponJE`
- executor digest: `sha256:1fdf17dbecb277e8e1e6601f6eb3807a04505422843dbe4654b1e0a1f1e6580b`
- authority key ID: `sha256:8437c03763990cf13ee7ce2e3d87ccb9ef1aa6d96ce3a8541a86d1d4f6e4e8fc`
- Caddy: `2.6.2`, active, valid, reachable, and configured for autosave resumption
- enabled operations: `stageArtifact`, `installGeneration`, `startCandidate`, `verifyCandidate`, `switchEndpoint`, and `verifyActive`

## Healthy baseline

Plan `sha256:da1236a0a6ce3314bf6c1ea7fc8ee8d765035e11bcaa43a25edc541547afc9b0` deployed the normal `v0.2.1` fixture. All six operations succeeded at fencing tokens 1 through 6. The active Generation was `provision-example-http-v0-2-1-4e775436b605` on private port `20087`, routed through stable port `18080`.

## Interruption after an Endpoint switch

Plan `sha256:31440f3380402e0e7d056b6b3aa4dc8d442bbb87be599c68f0bdd27211ec1690` prepared the separate `provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5` candidate on private port `26833`. Artifact staging, immutable Generation installation, systemd start, and direct candidate verification succeeded at fencing tokens 7 through 10.

The switch ran with a five-second execution lease. A local transport fault harness passed the signed `op-05` request to the real SSH client, waited for the remote executor to return success, withheld that response, and killed the local Provision process. It did not alter the host operation or its authorization. This produced the intended split state:

- the journal ended at sequence 21 with only the `op-05` intent for `attempt-67a79695b5603749610f9322da96b126`, fence 11;
- the host had durably selected the candidate and Caddy routed `provision-lab-web` to `127.0.0.1:26833`;
- the stable health response was HTTP 503, which is the fixture's deliberate stable-route failure mode;
- no fence-11 outcome existed in the State Backend.

After the lease expired, `deployment resume` selected `op-05` from the journal. A second tripwire would have killed the resumed process if it dispatched another host `execute`; it remained untouched. Fresh host observation instead proved the exact candidate unit, route, active-generation record, and retained previous Generation.

The resumed intent at sequence 22 recorded:

- attempt `attempt-abff84a4b217d98bea1586a9c76ccaab`;
- fence 12;
- `resumeOfAttemptId: attempt-67a79695b5603749610f9322da96b126`.

Sequence 23 recorded `succeeded` with the fresh `active` Endpoint observation. The already-completed traffic switch was not replayed.

## Interruption after an automatic rollback

The same fault was then applied to `op-06`. The remote executor observed the candidate's deliberate stable health failure, verified the exact signed previous Generation directly, restored its Caddy route, verified it again through stable port `18080`, and durably swapped the active and previous identities. The local process was killed after that host result completed but before it could append an outcome.

The resulting evidence was:

- journal sequence 24 contained only the `op-06` intent for `attempt-e74f762eeedc6a19f8824fb34ef2c232`, fence 13;
- stable `GET /verify` already returned `{"revision":"provision-example-http-v0-2-1"}`;
- the host's durable active Generation was already v0.2.1 and its retained previous Generation was the failed v0.3.0 candidate.

After the lease expired, `deployment resume` acquired fence 14 as `attempt-5c1f86d48eccff2f1a63a8214a2430ca`, with the fence-13 attempt recorded as its `resumeOfAttemptId`. The signed resumed verification recognized the completed rollback, rechecked v0.2.1 directly with 204, 204, and 200 responses, confirmed the Caddy upstream `127.0.0.1:20087`, and repeated the same three successful checks through the stable Endpoint.

Sequence 26 recorded the reconstructed result as `failed` with:

- `status: rolled-back`;
- `rollbackAttempted: true`;
- `rollbackSucceeded: true`;
- the exact restored Generation and observed upstream;
- both sets of fresh recovery checks;
- reason `an interrupted attempt already restored the previous Generation`.

The nonzero CLI result correctly prevented dependent work from treating the failed candidate as a successful Deployment.

## Final host state

Independent inspection reported `ready: true` with no findings:

- active: `provision-example-http-v0-2-1-4e775436b605`, route-matched to `127.0.0.1:20087`;
- retained previous: `provision-example-http-v0-3-0-fail-stable-b6f188a9b2f5`, still runnable on private port `26833` and not routed;
- stable `GET /verify`: `{"revision":"provision-example-http-v0-2-1"}`.

## Automated ambiguity and stale-writer evidence

Deterministic tests additionally cover states that should not be manufactured on the shared lab host: an ambiguous observation records `uncertain` with evidence and an actionable recovery instruction without applying the mutation; a late result from the interrupted lower fence is rejected and retained as non-authoritative audit evidence; a route matching neither signed Generation is uncertain; and a partial Caddy switch can be completed without loading the route again.

## Boundary

This evidence qualifies process-restart recovery for the implemented Host Deployment operations, journal-linked resume provenance, higher-fence takeover after lease expiry, observation-only completion of an already-finished switch, and reconstruction of an already-finished rollback.

It does not qualify role-specific drain completion, rollback-window cleanup, realtime connection recovery, or recovery for implementations other than the current systemd-and-Caddy Host path.
