# Valkey candidate-failure and forward-only boundaries — 2026-10-13

Live proof for #102, run direct-local on the disposable `provision-acceptance` VM (Ubuntu 26.04,
rootless Podman/Quadlet) via `scripts/run-direct-local-valkey-boundaries.sh`, which restores the clean
snapshot before each scenario and after the last. Source: the #102 scripts as committed on top of base `844563d`; the final runs
were made from that content before the follow-up commit that records it. State is CLI-provisioned: Valkey `9.1.1` is prepared, an acknowledged write
(`boundary:pre-transition`) is made, and the Environment is retargeted at `9.1.2`
([ADR 0077](../adr/0077-qualify-forward-only-host-valkey-transition-contract.md)).

| Scenario | Proof |
| --- | --- |
| `candidate-failure` | `prepareKeyValueCandidate` succeeds, the candidate unit is stopped, `synchronizeKeyValueCandidate` exits non-zero with journal outcome `failed`. The active `9.1.1` generation stays healthy with the same id, the pre-transition write and the deterministic workload keys are still readable, no `authority-switch.json` exists, and a direct `switchKeyValueAuthority` is refused with `operation dependency op-03 has no recorded outcome`. The checks are repeated after the refused switch. |
| `forward-only` | All six operations succeed; the Plan classifies rollback `forward-only`. A write acknowledged by the new `9.1.2` generation (`boundary:post-cutover`) is absent from the retained `9.1.1` data path, which does contain the pre-transition write; retention reports `retained-forward-only`. |

The resolved credential appears in no evidence for either scenario.

## Limits

- Candidate failure is one injection: the candidate stopped between prepare and synchronize. Failure
  during later phases is covered by the #100/#101 interruption matrix, not here.
- The previous-generation check greps its AOF files, so it proves absence on disk at that moment, not
  the impossibility of a later rollback replay. No rollback is claimed.
- Both scenarios were rerun together, end to end, on the final committed-content scripts after review
  fixes (an earlier `forward-only` run had failed on an over-specific string assertion that was replaced).
- Direct-local only; no SSH run. Raw evidence stays in the git-ignored `work/issue-102-live/`.
