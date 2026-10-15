# Plan approval and status

The initial local State Backend is a versioned SQLite database behind Provision's State Backend interface. It stores each canonical Plan unchanged, including its configuration and observation digests, selected capability evidence, exact operation inputs, approval requirements, and sensitive-value references. Approval decisions are append-only records with an actor, decision time, and expiry. Approval policy is evaluated outside the SQLite adapter.

Persisting a preview makes it the current Plan head for its Environment. This immediately supersedes an idle older Plan, whether or not the new Plan is approved. While the Environment has an unexpired, unreleased execution lease, persisting a different Plan is refused atomically: neither the new Plan nor a head change is saved. The running Plan remains current so its attempt can renew its lease and record an authoritative outcome. Persisting the same Plan and planning for other Environments remain available.

If a changed preview is refused, let the operation finish, or inspect and resume an interrupted operation before replacing its Plan. Then rerun `plan preview --file ROOT.yaml --state PATH` to obtain a fresh Plan from current observations. An expired execution lease no longer blocks replacement; expiry alone is not evidence that the Host operation succeeded or failed. For a read-only preview during execution, omit `--state`.

This defers head advancement under [ADR 0021](adr/0021-bind-plans-to-observed-state.md); it does not make a stale Plan eligible for new work. Approval always recompiles the supplied configuration and re-observes the Host Target before opening the State Backend. If the fresh Plan identity differs from the requested digest, approval fails without changing the database. Approval also fails if that exact Plan was not persisted by preview or is no longer the current head.

## Approve the previewed Plan

Create a private directory for local state, preview the Plan, and copy its `id` into the approval command:

```sh
mkdir -m 0700 .provision
go run ./cmd/provision plan preview \
  --file examples/host-http/root.yaml \
  --state .provision/state.db
go run ./cmd/provision plan approve \
  --file examples/host-http/root.yaml \
  --plan sha256:PLAN_DIGEST \
  --actor ACTOR \
  --state .provision/state.db \
  --expires-after 15m
```

The State Backend file is created with mode `0600` and later opens reject weaker permissions; its parent directory must already exist. In this local-first slice, the operating-system account authenticates the actor and owner-only access to the State Backend supplies the external `approve` Operation Capability grant. Each grant is scoped to the exact Plan's Application and Environment, and the Plan's approval requirements are evaluated before the decision is recorded. `--actor` must match that authenticated OS username; it cannot assign an arbitrary identity.

## Single-operator and mediated-agent boundaries

The workflow above remains supported as single-operator use: the operator can approve its own Plan and execute it using its local signing key. Giving an agent the same OS account, State Backend, and signing key gives it that authority too.

The `request` commands implement the separate boundary in [ADR 0079](adr/0079-mediate-agent-requests-through-independent-authority.md). A requester submits identifiers through its registered filesystem inbox. The independently authenticated operator captures the request, resolves captured trusted configuration, observes the remote Host, reviews the exact Plan and selected operation, and records a request-specific decision. Only the bound `stageArtifact` operation can execute. Use a separate private State Backend for mediation: a Plan associated with a request cannot be dispatched through generic `deployment execute` or `deployment resume`, even if it also has a Plan-wide approval.

The first implementation supports the remote native HTTP Host configuration accepted by `HostSelection`, with one `stageArtifact` operation. It adds no daemon or network API. Linux OS ownership and permissions provide isolation; the OS, administrator, trusted operator, and Host verifier remain trusted. An agent with their credentials or process control is outside this boundary. Approval of an Artifact does not establish the safety of its executable contents.

### Provision accounts and registration

An administrator supplies distinct unprivileged requester and operator accounts, private primary groups for requesters, and operator-only signing and SSH credentials. The example names match the acceptance exercise. The requester must have no sudo, authority key, private State Backend, Host credentials, access to the operator's processes, or write access to the operator's binaries, HOME or PATH. Do not forward the requester's agent or environment into the operator session.

Run the following administrative setup on a controller, after creating `authority` and `requestbot` accounts:

```sh
sudo usermod -aG requestbot authority
sudo install -d -m 0755 /srv/mediation /srv/mediation/inbox /srv/mediation/results
sudo install -d -o authority -g authority -m 0700 /srv/mediation/private
sudo install -d -o authority -g requestbot -m 2730 /srv/mediation/inbox/requestbot
sudo install -d -o authority -g requestbot -m 2750 /srv/mediation/results/requestbot
```

Log in afresh as `authority` so its supplementary groups take effect. Create `/srv/mediation/private/registry.json` with mode `0600`. Replace the UID and GID with `id -u requestbot` and `id -g requestbot`; the importer verifies both against the OS account. Additional requesters need distinct accounts, primary groups, inboxes and results directories.

```json
{
  "schemaVersion": "provision.dev/request-registry/v1alpha1",
  "operator": "authority",
  "requesters": {
    "requestbot": {
      "account": "requestbot",
      "uid": 1002,
      "gid": 1002,
      "inbox": "/srv/mediation/inbox/requestbot",
      "results": "/srv/mediation/results/requestbot",
      "grants": [{
        "application": "provision-example-http",
        "environment": "lab",
        "operations": ["stageArtifact"]
      }]
    }
  },
  "configurations": []
}
```

Put the trusted root YAML and referenced documents under the operator's private directory, owned by `authority` and not writable by another account. Configure the remote Host and its operator there; configure strict SSH host-key verification in `authority`'s private HOME. Bootstrap the Host with the existing restricted executor and the public half of the authority key. Keep the private key on the controller.

As `authority`, register and capture all configuration inputs, including Artifact source and digest:

```sh
provision request register --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --file /srv/mediation/private/configuration/root.yaml
```

Registration stores the validated compiled configuration in the private registry. Import captures it again with the request; planning and execution never reload the original YAML. Re-registering the same Application/Environment/Revision tuple with different inputs is refused: register a new Revision identity and submit a new request. Changing or removing a registered configuration or requester grant also invalidates the old capture for further dispatch. The registry and SQLite file must be distinct absolute paths in the same owner-only directory. Trusted paths reject symlinks and directories controlled by other accounts. Inbox files must be single-link regular files, at most 64 KiB, owned by the registered requester, and not writable by another account.

### Submit, review and decide

As `requestbot`, create this JSON in a file called `stage-001.json`. `id` is the requester's unique submission identifier; use a new one for intentional repetition.

```json
{
  "schemaVersion": "provision.dev/operation-request/v1alpha1",
  "id": "stage-001",
  "application": "provision-example-http",
  "environment": "lab",
  "revision": "provision-example-http-v1",
  "component": "web",
  "operation": "stageArtifact"
}
```

```sh
provision request submit --inbox /srv/mediation/inbox/requestbot --file stage-001.json
```

A claimed actor, extra field, duplicate JSON key, path reference, or another operation kind is rejected. A filename supplies no identity. The trusted importer authenticates the file's OS ownership against the registered inbox and account, checks for replacement or in-place modification during capture, and records immutable bytes and their digest privately.

As `authority`, import the filename, then use the returned `capture.id`:

```sh
provision request import --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --requester requestbot --name stage-001.json
provision request plan --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --request sha256:REQUEST
provision request review --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --request sha256:REQUEST
```

Human review includes the authenticated requester and scope, captured configuration and Artifact identities, full exact Plan, selected `operationId`, and `operationDigest`. The original submission bytes are preserved as base64 in the private capture. The reviewer must confirm the displayed Plan and operation in an explicit decision under the independently authenticated operator account:

```sh
provision request approve --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --request sha256:REQUEST --plan sha256:PLAN --operation op-01 --actor authority --expires-after 15m
```

Use `request deny` with the same arguments to record a denial. `--actor` confirms the OS username; it cannot impersonate another user. A requester account cannot decide its own mediated request. Approval is append-only and binds the request, Plan, operation digest, signing-key identity, actor, decision time and expiry. A Plan-wide approval is insufficient. Approval re-observes the Host and rejects stale Plans.

### Execute and recover

As `authority`, dispatch exactly the approved operation:

```sh
provision request execute --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --request sha256:REQUEST --signing-key /srv/mediation/private/authority.key
provision request status --registry /srv/mediation/private/registry.json --state /srv/mediation/private/state.db --request sha256:REQUEST
```

Dispatch rechecks current grants, captured input identities, Host observations, Plan head and the narrow approval. Signing and dispatch recheck authorization; the signed claim expires no later than approval and retains the existing five-minute maximum. The same engine, typed envelope, dependency checks, fenced lease and journal are used. The request's approval cannot authorize installation or activation from the same Plan.

After interruption or an uncertain result, use `request resume` with the same request and signing-key arguments. The engine observes the bound operation before any replay. A known failure requires another explicit human decision before `request execute` may retry. Completed duplicates return the recorded outcome without another attempt; a new operation needs a new request and approval. Formatting-only differences in an otherwise identical resubmission preserve the original captured bytes and identity.

Approval expiry prevents new dispatch or redispatch. Already-dispatched bounded work may finish, renew its execution lease, and record its outcome without issuing new authorization. Read-only observation remains available through `request observe --registry ... --state ... --request ...`, including after expiry. Recovery also rechecks staleness: a relevant change requires a new request, Plan and approval.

Request capture, planning, decisions, execution and status publish a requester-specific sanitized receipt under `/srv/mediation/results/requestbot/REQUEST_HEX.json`; `request status` refreshes approval status. The requester can read that file directly without State Backend access. Receipts contain scope and input digests, the Plan and operation binding, approval status, signing-key and attempt identities, fencing tokens and outcomes. Private capture, full Plans, raw Host observations, transport diagnostics and other requesters' records are not published. Dispatch refusals also leave an attributable private request event and a sanitized event in the receipt. Publication failure is reported; rerun `request status` to publish from the authoritative journal without dispatching again.

Live qualification is recorded in [the isolated-account evidence](evidence/2026-10-15-mediated-stage-artifact.md) and is limited to its tested combination. Run `scripts/run-mediated-stage-acceptance.sh` to reset and reproduce the exercise; its `--mediated` entry point extends `scripts/test-remote-ssh-host.sh`. It restores the disposable Host snapshot, creates a separate disposable controller, and cleans both up after success. It is not a production setup command.

## Inspect approval and eligibility

```sh
go run ./cmd/provision plan status \
  --plan sha256:PLAN_DIGEST \
  --state .provision/state.db
```

Status opens an existing backend and applies any supported schema migration before reading it. It reports the immutable Plan, approval actor and times, decision, and eligibility. A persisted preview begins as `unapproved`. An approval is eligible only while it is approved, unexpired, and still the Environment's current Plan head. Successfully persisting a different preview for that Environment marks the older Plan `superseded` without deleting either its Plan bytes or approval history. A refused preview leaves status unchanged.

Eligibility is necessary but is not itself host execution authorization. Execution acquires a fenced execution lease for the Environment mutation, journals its intent, and sends a separate short-lived signed authorization for one exact operation to the restricted host executor. This is distinct from the optional team-member **Environment Lease** in the product model. See [Authorized release preparation](release-preparation.md).
