# Off-host `ssh://` PostgreSQL backup, live — 2026-10-13

Live proof for #94 (parent #92, [ADR 0078](../adr/0078-execute-off-host-ssh-database-backup.md)).
`scripts/run-offhost-ssh-postgresql-acceptance.sh` runs `backupDatabase` and `verifyDatabaseRestore` from the
`provision-acceptance` VM's managed PostgreSQL `17.6` to a **second Incus VM**, `provision-backup-dest`
(`192.168.101.149`, same `base.local` Incus host, own host keys and `pgbackup` account), over **direct-local** and
**strict-SSH**. Three scenarios per transport, each from the clean snapshot of **both** VMs:

| Scenario | Fault | Result (both transports) |
| --- | --- | --- |
| `success` | none | backup and isolated restore verification `succeeded` |
| `unreachable` | destination VM stopped after the Database is provisioned | `backup-destination-unreachable`, outcome `failed` |
| `host-key-mismatch` | destination's SSH host keys regenerated | `backup-destination-host-key-mismatch`, outcome `failed` |

Structured record: [JSON](2026-10-13-postgresql-offhost-ssh.json). Raw per-run evidence stays in the git-ignored `work/`.

## What the success runs show

- The backup and restore evidence record `destinationHost` `192.168.101.149`, `hostKeyFingerprint`
  `SHA256:4aNfhV4L/iOg0a0Ci+PuTjPJNYd0Hqs9lqhbyH8chsY` (the pin) and an identical `artifactDigest`.
- **Independent destination-side check:** `sha256sum` run on the destination VM equals that `artifactDigest`
  (`destinationObservedSha256` in the JSON); the dump is `0600 pgbackup`.
- The backup private key (a Secret Reference) appears nowhere in the evidence directory.
- Failure runs record the failed outcome in the journal, with no weaker-check retry and no `file://` fallback.

## Defect found by the live run

The first run failed: every `ssh://` backup or restore operation was rejected with
`prepareDatabase requires exactly one resolved Secret Reference`. `validateDatabaseSensitiveValues` required
exactly one resolved secret for all Database operations, but an `ssh://` backup/restore also carries the backup key
reference (`resolvedValuesForOperation` correctly supplies both). The fake-`ssh` shim tests of #92 never reached that
check. Fixed in the shared validator: the Database reference, plus the declared backup reference only, and nothing else.

## Limits

- **Source:** the runs used base commit `133e967` plus the uncommitted validator fix above (`-dirty-ef3917bb…` in the JSON).
- **Both VMs share one physical Incus host and LAN.** This proves a separate machine identity, host key and SSH
  endpoint, not independent hardware or network failure domains.
- The destination is a newer Ubuntu image (`20261012`) than the acceptance VM (`20260924`); the original image had been pruned.
- Not re-run live here: partial upload and response loss after upload (covered by the executor's fake-`ssh` tests).
- Unreachable is a stopped VM; host-key mismatch is key regeneration on the same address.
- Backup uses the plain `examples/host-database` (no HTTP workload); Store Transition is not part of this proof.
