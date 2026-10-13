# Execute off-host `ssh://` Database backup

**Status: Accepted — 2026-10-13.** (Issue #92 asked for number 0076; that number was already taken by the Valkey packaging ADR.)

## Context

[ADR 0075](0075-prove-host-postgresql-backup-and-isolated-restore-verification.md)
recognized `ssh://` backup destinations as the off-host contract shape but the
Host renderer rejected them, so `hostLoss: off-host-backup-required` could be
declared yet never executed.

## Decision

`backupDatabase` and `verifyDatabaseRestore` execute against an `ssh://`
destination:

- **Config.** `backup.sshUser`, `backup.hostKey` (a `SHA256:...` fingerprint pin)
  and `backup.credentialReference` (private key as a Secret Reference) are all
  required for `ssh://` and rejected for `file://`. URL userinfo stays rejected.
  User and pin are non-secret and appear in the Plan. Only the key is resolved
  for these two operations (`resolvedValuesForOperation`).
- **Transport.** System `ssh` with `BatchMode=yes`, `StrictHostKeyChecking=yes`
  and a temporary `UserKnownHostsFile` holding only the key from `ssh-keyscan`
  whose fingerprint equals the pin. The key and known-hosts files live in a
  0700 scratch directory under the executor's (root) temp dir and are removed afterwards. Real
  `known_hosts` is never touched, and no credential is presented to a host
  that does not match the pin.
- **Upload.** Stream to `<id>.dump.partial`, compare the remote `sha256sum` with
  the local digest, then rename to `<id>.dump`. Linux destinations only.
- **Evidence.** `<id>.json` is written beside the dump after the rename and read
  back. `DatabaseBackupObservation` gains `artifactDigest`, `destinationHost`
  and `hostKeyFingerprint`; `Verify` requires all three for `off-host-ssh` and
  requires the fingerprint to equal the declared pin. Restore evidence carries
  the same digest and host identity.
- **Restore verification.** Fetch the dump to a scratch path, recompute the
  digest, refuse to restore on mismatch, then restore into the isolated
  candidate as before.
- **Failures.** Unreachable, host-key mismatch, auth failure, digest mismatch
  and partial upload are categorized `backup-destination-*` `failed` outcomes
  with no weaker-check retry and no `file://` fallback. Only losing the
  response after the dump may have landed (rename or evidence step) is
  `uncertain`.
- **Recovery observes before acting** ([ADR 0047](0047-observe-before-retrying-uncertain-operations.md)): recorded evidence
  with a matching digest is reused; a final name without evidence is adopted
  (evidence written from the destination's digest, no re-upload); otherwise any
  `.partial` is removed and the upload retried.

## Consequences

- Host-loss declarations are executable, but the recovery point of an adopted
  upload is the dump's mtime, which trails the true backup start by the upload
  duration.
- The live acceptance-VM proof (direct-local and strict-SSH, second host as
  destination) is tracked separately; until it passes the support row is
  **unqualified**.
- Adopting a final-name upload and the restore digest check trust the destination's own digest; there is no independent anchor beyond the pinned host identity.
- The dump is buffered in memory during upload; very large Databases need streaming first.
- Host-key rotation requires a new pin in config and therefore a new Plan.
