# PostgreSQL second image and CLI-provisioned Store Transition — 2026-09-29

Issue #84. Qualifies PostgreSQL `17.7` for Host-local packaging and proves the
forward-only Store Transition from `17.6` to `17.7` starting from state
produced by the public CLI, on the disposable `provision-acceptance` VM
restored from its clean snapshot.

## What ran

- Packaging proof for `17.7` via `scripts/run-postgresql-quadlet-acceptance.sh`
  (approval record `2026-09-29-postgresql-17-7-packaging-approval.json`).
  Qualification digest: `sha256:0b786851c1ab07c183650c91dd3ad8bbbe8c74c11e9ba535d585eb0841718ad0`,
  recorded as `Evidence` for the `17.7` entry in `hostdatabase.Images`.
- `scripts/run-direct-local-postgresql-acceptance.sh` with fault modes `before`,
  `after`, and `undecidable`. Each mode resets and bootstraps the VM, provisions
  `17.6` through the CLI, previews and approves a Plan with `version: "17.7"`,
  and executes the Store Transition with interruption recovery. All three
  passed; each ended with active generation `postgresql-17-7-030da09481c3` and
  retained generation `postgresql-17-6-b86568d3e0fe`.
- Source: commit `e1bb459` plus the working-tree changes committed with this
  evidence; executor `sha256:f01e4cc83af328ec06451ebe13059ec0820707ff124e00172e36c51c7d02b49a`.

## Undecidable post-switch observation

After `switchDatabaseAuthority` completed and the controller was interrupted,
the harness replaced the authority-switch evidence file with a directory.
Resume observed the evidence as unreadable (not absent), refused to replay the
switch, and recorded outcome `uncertain` with reason "Database authority-switch
evidence cannot be read; the switch may have landed" and a recovery action. After
the evidence was restored, resume completed successfully.

## Defects found and fixed by this run

- A transition after an earlier restore verification for a different image
  failed because the leftover restore-verification Quadlet was treated as an
  exact-match file. It is scratch state and is now replaced.
- The transition Plan redeploys the HTTP workload, whose candidate port is
  derived from the artifact and collided with the still-active unit; the
  harness now resets only the HTTP workload (no Database state) before the
  transition.

## Not qualified

Other PostgreSQL versions or images, cross-major transitions, remote SSH
execution of this matrix, host-loss recovery, and production data migration.
