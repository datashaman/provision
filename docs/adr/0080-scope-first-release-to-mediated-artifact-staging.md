# Scope the first useful release to mediated Artifact staging

**Status: Accepted — 2026-10-15.** The maintainer selected the narrow access and
execution proxy scope for [issue #126](https://github.com/datashaman/provision/issues/126).
This supersedes ADR 0032's initial-release requirement for all seven deployment
scenarios and a required-blue-green-capable path for every Component role.
It also amends ADR 0029's initial target sequencing: AWS and full local/remote
deployment coverage remain the roadmap. Capability Contracts and fail-closed
validation remain required.

The first useful release proves the independent authority boundary in
[ADR 0079](0079-mediate-agent-requests-through-independent-authority.md): an
isolated requester account submits an Operation Request for a registered
Application, Environment, Revision, and native HTTP `stageArtifact`; a separate
trusted human operator reviews and approves the exact request, Plan, and
operation; the restricted executor on a separate remote Host Target enforces
short-lived typed authorization. Success means the approved bytes are in the
digest-addressed cache with matching request-linked journal and sanitized
receipt evidence. It does not authorize installation, activation, or arbitrary
execution, and digest verification does not establish executable safety.

The local filesystem handoff, trusted CLI, local SQLite State Backend, and
on-demand execution are sufficient. Requester credentials must not reach
approval, signing, private state, or direct Host access. Scope checks, immutable
capture, Plan staleness, exact-operation approval, expiry, fencing, replay
protection, observation before recovery, fresh approval after known failure,
completed-request deduplication, and private result publication remain release
requirements. Already-dispatched bounded work may finish after approval expiry;
new dispatch or redispatch requires valid approval.

The [isolated-account proof](../evidence/2026-10-15-mediated-stage-artifact.md)
qualifies this boundary for its exact Ubuntu controller/account and remote Host
combination. Its root supervisor exercises the independently authenticated human
approval entry point; it does not prove physical human presence or protect a
compromised trusted operator or administrator. Broader Host Deployment evidence
remains valid within its own scope, and existing single-operator capabilities
and their safety and recovery guarantees remain supported separately.

Full local and remote-host application deployment, EC2 with host-local services,
EC2 with AWS-managed services, ECS/Fargate, Lambda, and hybrid scenarios remain
the deployment roadmap rather than the first-release gate. Complete Component
coverage, shared AWS state, hosted authority, automated approval, and mediation
of other operations or Host combinations are deferred. They need their own
qualification before being claimed; no existing capability is removed.

One independently authorized operation tests the access-proxy thesis directly.
Requiring unrelated adapters first would delay that test without strengthening
this boundary. The [first-release checklist](../first-release-checklist.md)
records the executable gate and publication work; accepting this scope does not
declare a release published or broaden the existing support evidence.
