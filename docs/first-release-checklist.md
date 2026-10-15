# First useful release checklist

[ADR 0080](adr/0080-scope-first-release-to-mediated-artifact-staging.md) selects
mediated native HTTP `stageArtifact` on the qualified remote Host combination
as the first useful release. The seven full deployment scenarios remain future
work. This checklist records acceptance and publication separately; it is not a
declaration that a release has shipped.

## Executable acceptance

- [x] A real isolated requester account submits an Operation Request through its
  filesystem inbox, referencing a registered Application, Environment, Revision,
  and `stageArtifact`. Identity comes from OS ownership and trusted registration.
- [x] A separate trusted operator owns configuration, grants, SQLite state,
  signing, and Host credentials. The human approval command authenticates that
  account and binds the exact captured request, Plan digest, and operation.
- [x] The separate remote Host's restricted executor verifies the short-lived
  typed authorization and approved Artifact digest. Only cache staging succeeds;
  installation and activation remain unauthorized by this approval.
- [x] Out-of-scope and unapproved requests, requester bypass attempts, altered
  inputs, revoked grants, and other-operation attempts fail without unauthorized
  Host mutation. Private state and other requesters' results stay inaccessible.
- [x] Approval expiry blocks dispatch and redispatch; already-dispatched bounded
  work may finish. Read-only observation remains available. Interrupted or
  uncertain work is observed before recovery, with request-bound attempts and
  fencing; known failure requires a fresh human decision; completed duplicates
  return the recorded outcome.
- [x] Independent Host digest observations agree with the request-linked journal
  and sanitized receipt; source, binary, account, target, command, and exit-code
  evidence is recorded. No application Generation, service, or Endpoint changes.

These checks passed in the [2026-10-15 live proof](evidence/2026-10-15-mediated-stage-artifact.md)
and its [structured results](evidence/2026-10-15-mediated-stage-artifact.json):
15 case groups and 117 command exit assertions. The proof covers the stated
Ubuntu 26.04.1 controller/account setup and separate Ubuntu 26.04 Host over strict
SSH, sharing a physical host and LAN. The root test supervisor exercises the
human approval entry point under the trusted account, not physical human
presence or resistance to a compromised trusted operator/administrator.

To reproduce on the explicitly disposable lab, use the proof's prerequisites,
reset instructions, and `scripts/run-mediated-stage-acceptance.sh`. It resets
the named Host VM and creates/deletes a controller VM. For an actual operator
workflow, use [Plan approval and status](plan-approval.md) and the
[mediation contract](agent-request-mediation.md).

## Before publishing a release

- [ ] Pin the release source and binary identities. Confirm that acceptance
  evidence covers those production sources and the advertised target/account
  setup; rerun the live exercise for changes affecting the boundary or operation.
- [ ] Pass the repository's CI and required regression checks, retaining the
  existing single-operator Host capabilities and their documented guarantees.
- [ ] Publish signed versioned archives, checksums, a software bill of materials,
  and the matching OCI runner image required by the
  [distribution decision](TECHNICAL_ARCHITECTURE.md#signed-provision-releases).
- [ ] Publish release notes linking the exact proof and
  [support matrix](SUPPORT_MATRIX.md). State that mediated support is only the
  qualified `stageArtifact` combination; local mediated Hosts, other operations,
  other OS combinations, hosted authorities, and automated approval are deferred.
  Staging verifies bytes, not executable safety or full application deployment.

Existing broader single-operator evidence does not qualify mediated approval
for those operations. Future adapters and deployment scenarios retain their
Capability Contract, blue-green, data-safety, audit, and recovery requirements
where claimed; they need separate evidence before support is advertised.
