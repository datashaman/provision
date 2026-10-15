# Mediate agent requests through independent authority

**Status: Accepted — 2026-10-15.** The maintainer confirmed shared understanding
after the [issue #124](https://github.com/datashaman/provision/issues/124) design
interview. Implementation and scoped live qualification are recorded in
[the isolated-account evidence](../evidence/2026-10-15-mediated-stage-artifact.md).

Provision will support agent requests through an independent authority boundary:
request access alone must not permit an agent to approve or sign its own
operation, or bypass approval through direct Host Target access. This is a
product requirement even when the requesting agent is compromised, rather than
a convention enforced by its instructions. It builds on the Operation
Capabilities in [ADR 0027](0027-authorize-operation-capabilities.md) and the
exact-Plan, short-lived typed Host authorization in
[ADR 0070](0070-require-plan-bound-host-authorization.md).

The existing single-operator workflow remains supported: that operator may
approve and execute its own Plans. It must be documented separately from
mediated-agent use and does not prove requester isolation.

Approval, signing, and execution may share one trusted authority boundary;
three independently operated services are not required. The requesting agent
must not have access to that boundary's credentials, State Backend, or process
control. The first mediated workflow requires an independently authenticated
human to approve the exact Plan; automated approval policy is deferred.

The agent submits an Operation Request referencing a registered Application,
Environment, Revision, and permitted operation. The trusted side owns target
configuration, observes the Host Target, and generates the Plan for human
review. A requester-supplied Plan is not authority to sign operations or change
trusted configuration or capability grants.

The first proof uses isolated requester and trusted operator accounts, an
explicit request handoff, and on-demand trusted execution. The operator holds
the State Backend, signing key, and Host credentials; the requester cannot
control that account or its processes. No resident service or new network API
is required. The first operation is the existing `stageArtifact`: download the
approved Artifact, verify its digest, and populate the Host cache. This proves
the authority boundary without deciding the first-release scope in issue #126.

Trusted policy grants each requester explicit Application and Environment
scope and allowed operation kinds. Host selection comes exclusively from
trusted configuration. Out-of-scope requests fail before approval; human
approval does not silently expand requester permissions.

Before human review, the trusted side captures the immutable Operation Request
and its resolved input identities. Execution uses that capture and the exact
approved Plan, never a mutable requester file. Changed inputs require a new
request or Plan and fresh approval; existing Plan staleness checks remain in
force.

Mediated approval binds the request identity, exact Plan digest, and selected
operation. The existing Plan-wide approval record alone is insufficient to
authorize mediated execution. A `stageArtifact` request cannot authorize
installation or activation, even when those operations appear in the same Plan.
Recovery may resume the same journaled operation under existing recovery rules;
another requested operation requires its own approval.

Approval expiry blocks new dispatch and redispatch, while already-dispatched
bounded work may finish and record its outcome. Read-only observation remains
available; further mutation requires valid approval. Host authorization retains
its short lifetime and replay protection. A known failed operation requires a
fresh human decision before retry; interrupted or uncertain work may resume
the same operation under still-valid approval and existing recovery rules.
Resubmitting a completed request returns its recorded outcome. Intentionally
performing the operation again requires a new request and approval.

The initial handoff is a local per-requester filesystem inbox. The trusted
importer establishes requester identity from OS ownership and trusted account
registration, verifies the submitting account, rejects unsafe file references,
and captures submitted bytes in private storage. A claimed username in the
request is not authentication. The human approves under the separate trusted
OS account. Requesters need neither signing keys nor Host credentials.

Requesters receive read-only access to their own receipt, Plan summary,
approval status, and sanitized execution outcome, published separately from the
private State Backend. Evidence includes request, Plan, operation, and attempt
identities without secrets or other requesters' records.

Acceptance requires a real isolated requester account and a separate remote
Host Target. It must prove that out-of-scope and unapproved requests cause no
Host mutation; that the requester cannot access authority credentials, write
trusted state, impersonate the approver, or invoke the Host executor directly;
and that independent human approval permits exactly the requested
`stageArtifact` operation with matching digest and audit evidence. Altered
requests, expired approval, and attempts to execute another operation must be
rejected. Interrupted execution must preserve the existing observation and
recovery rules. In-process permission tests alone do not establish this
boundary.

The current local authority adapter and operator-held signing key do not
establish this separation when both are available to the agent. The `request` commands now implement the separate filesystem workflow. See the
[approval guide](../plan-approval.md) for actual commands and the
[implementation contract](../agent-request-mediation.md) for its acceptance
requirements. Scoped proof is tracked by
[implementation issue #130](https://github.com/datashaman/provision/issues/130).
The release-scope decision in issue #126 remains
separate.
