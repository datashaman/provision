# Agent request mediation implementation contract

Implement the independent authority boundary selected in
[ADR 0079](adr/0079-mediate-agent-requests-through-independent-authority.md)
for [issue #124](https://github.com/datashaman/provision/issues/124). This is a
follow-up implementation brief for the design accepted on 2026-10-15.
The filesystem workflow is implemented by the `request` commands documented in
[the approval guide](plan-approval.md). Live qualification is limited to the
combination and acceptance cases recorded in
[the isolated-account evidence](evidence/2026-10-15-mediated-stage-artifact.md).
Implementation and live qualification are tracked in
[issue #130](https://github.com/datashaman/provision/issues/130).

## First scenario and credential custody

An isolated requester account on a controller submits an Operation Request to
stage one registered Revision's Artifact on a separate remote Host Target.
The trusted operator imports it, generates the Plan from trusted inputs and
fresh Host observations, reviews it as a human, and approves that request's
exact Plan and selected `stageArtifact` operation. An on-demand runner executes
only that operation and publishes its sanitized outcome.

| Principal | Access and credential custody |
| --- | --- |
| Requester | Separate OS account; may submit through its registered inbox and read its own published results. Has no authority signing key, private State Backend access, Host credentials, executor sudo permission, or control of trusted processes. |
| Human approver | Independently authenticated through the trusted operator OS account. Reviews the immutable request and exact Plan and records approval and expiry. The requester cannot invoke commands as this account. |
| Signer and runner | May use the same trusted operator account as approval. Holds private state, signing key, and remote Host credentials, and invokes the existing engine on demand. |
| Host executor | Retains its root-owned public verifier and restricted typed-operation enforcement. The private authority key stays on the controller. |

The OS, trusted operator, signer, and Host verifier remain trusted. This design
protects against a compromised requester account, including attempts to bypass
the normal CLI. It does not provide separation from a compromised trusted
operator or administrator. Existing single-operator use remains supported and
explicitly documented.

## Implementation requirements

1. Add a strictly validated, versioned Operation Request format referencing
   registered Application, Environment, Revision, and requested operation.
   Resolve references through trusted registration; requester fields cannot
   select arbitrary configuration paths, Host credentials, policy, or keys.
   Bind registered Revision inputs to captured content identities, including
   Artifact source and digest; a mutable Revision name alone is insufficient.
2. Implement a local per-requester filesystem inbox and one-shot trusted
   importer. Authenticate from verified OS ownership and trusted account
   registration, not a claimed actor field or filename. Keep inbox registration
   and trusted directories outside requester control. Safely capture bounded
   regular-file input into private immutable storage; reject unsafe links,
   path traversal, ownership mismatches, and substitution races. Do not load
   additional requester-controlled files while planning or executing.
3. Enforce explicit per-requester Application/Environment grants and allowed
   operation kinds before seeking approval and before dispatch or redispatch.
   Host selection comes from trusted configuration. Unknown references and
   out-of-scope requests fail closed; human approval cannot implicitly expand
   those grants.
4. Generate the Plan on the trusted side using captured configuration and fresh
   Host observations. Present the request, scope, resolved input identities,
   exact Plan, and selected operation for human review. Capture the approval
   actor, decision time, expiry, request identity, Plan digest, and exact
   operation identity and digest in trusted state. A pre-existing Plan-wide
   approval cannot substitute for this mediated approval.
5. Check that binding before signing and use the existing execution engine,
   typed envelope, fencing, dependency checks, and journal. A request for
   `stageArtifact` cannot execute installation or activation operations in the
   same Plan. Keep the existing short authorization lifetime and replay checks;
   issued authorization must expire no later than the mediated approval.
6. Preserve Plan staleness checks. Changed inputs require a new request or
   Plan and fresh approval. Approval expiry blocks new dispatch or redispatch;
   already-dispatched bounded work may finish and record its outcome. Allow
   read-only observation after expiry. Resume only the same interrupted or
   uncertain operation under valid approval, observing before mutation. Known
   failure needs a fresh human decision before retry. Duplicate submission of
   a completed request returns its recorded outcome; intentional repetition
   needs a new request and approval.
7. Publish requester-specific read-only receipts, Plan summaries, approval
   status, and sanitized outcomes separately from private state. Preserve the
   link between authenticated requester, captured request, human approval,
   Plan, operation, signing-key identity, execution attempts, and host evidence.
   Denials and failures remain attributable. Do not expose secrets, raw
   private state, or other requesters' records.

## Existing seams and required changes

- `cmd/provision/main.go` has the existing CLI dispatch and live planning path.
  Add only the request handoff and trusted commands needed for this scenario;
  requester-side live planning currently requires Host access.
- `internal/config` loads mutable YAML paths. It does not currently implement
  trusted registration or immutable request input capture.
- `internal/approval` provides capability policy and local OS authentication.
  Keep that authentication for trusted operator use; request fields must not
  construct their own authenticated authority.
- `internal/state` stores exact Plans, Plan-wide approvals, and the fenced
  journal. Extend versioned persistence as needed for request attribution,
  narrow approval binding, and idempotent receipts. Preserve existing formats
  and the single-operator flow.
- `internal/execution` checks approval before signing; `internal/authority`
  signs claims without consulting approval policy. Mediation must enforce its
  narrower binding on every signing path, including recovery.
- `cmd/provision-host-executor` already enforces signed typed operations. Keep
  this contract and its root-owned verifier; the Host trusts the signer to
  attest authorization rather than reading the controller's approval state.
- Reuse `scripts/test-remote-ssh-host.sh` for controller-side preview,
  execution, and outcome checks, and existing disposable-VM reset/bootstrap
  patterns. Existing harnesses use one operator and do not prove isolation.

## Required verification and evidence

Add focused tests for ingress identity and filesystem attacks, immutable input
capture, grant enforcement, request/Plan/operation approval binding, expiry,
duplicate requests, and recovery. Tests must exercise the actual enforcement
paths rather than constructing an already-trusted authority in process alone.

Run a reproducible acceptance exercise with a real isolated requester account,
a separate trusted operator account, and a separate remote Host Target:

| Case | Required result |
| --- | --- |
| Out-of-scope request | Denied before approval, with attributable evidence and no Host mutation. |
| In-scope request without approval | No dispatch or Host mutation. |
| Requester bypass attempts | Cannot read signing/Host credentials, modify trusted configuration or state, impersonate the approver, or invoke the Host executor directly. |
| Approved `stageArtifact` | Only the exact selected operation succeeds; cached bytes match the approved digest and request-linked journal and published result agree. |
| Another operation in the approved Plan | Installation or activation is denied under the staging request's approval. |
| Modified submission or resolved inputs | Cannot change the captured request or broaden approved work; changed identities require fresh review. |
| Expired approval | New dispatch and redispatch are denied; read-only observation remains possible. |
| Expiry after dispatch | Bounded work may finish and record its outcome; no additional mutation is authorized by the expired approval. |
| Interruption or uncertain outcome | Observe before retry; resume only the bound operation under valid approval, preserving fencing and attempt evidence. |
| Known failure or completed resubmission | Failure is not retried without a fresh human decision; a completed duplicate returns the recorded outcome. |
| Requester result access | Own published results are readable and cannot be changed; private state and another requester's results remain inaccessible. |

Record the source revision, environment and account setup, exact runnable
commands, expected and observed outcomes, exit codes, Host before/after
observations, and sanitized request/approval/journal evidence in `docs/evidence/`.
Include instructions to reset and rerun the exercise. Mark mediation qualified
only after this proof passes, and update the approval guide with real commands.

## Scope limits

This follow-up does not require a resident service, network API, hosted
coordinator, new identity provider, automated approval policy, general-purpose
shell access, additional Host operations, or new execution adapters. It does
not itself decide the first-release gate; [ADR 0080](adr/0080-scope-first-release-to-mediated-artifact-staging.md) records that separate product decision for issue #126. Approving an Artifact does not establish the safety of its executable contents. Keep the existing
single-operator workflow working and describe its distinct trust boundary.
