# Isolated-account mediated `stageArtifact`, live — 2026-10-15

Live proof for [#130](https://github.com/datashaman/provision/issues/130), implementing [ADR 0079](../adr/0079-mediate-agent-requests-through-independent-authority.md). **Qualified only for the combination below:** the local filesystem handoff on an Ubuntu 26.04.1 controller, explicit trusted-operator approval, and the existing typed `stageArtifact` operation against a separate Ubuntu 26.04 Host VM over strict SSH. This does not expand the release gate in #126.

The clean reset-and-rerun exercise passed all 15 case groups and all 110 command exit-code assertions. `go test ./...`, `go vet ./...`, race tests for mediation/execution/state, shell syntax checks and ShellCheck passed. The Host was restored to its clean snapshot and the disposable controller was deleted after success.

Structured evidence: [JSON](2026-10-15-mediated-stage-artifact.json), including every exact command and observed exit code, authenticated capture summaries, sanitized receipts and approval bindings, recovery journal events, and independent Host observations. Raw command output remains in git-ignored `work/issue-130-live/evidence/`; it contains only this exercise's synthetic inputs.

## Source, environment and credential custody

- Base revision: `140cf799b918e2358fb8c8cd74493ceb0aa0dd69`; staged production source tree: `c9b8c6c9e2a8e895820c6baee56265317b5afe6b`. The JSON records SHA-256 identities for every changed production source file and `go.mod`; these matched that tree at evidence capture. Documentation and additional harness assertions were completed after that source snapshot; the transferred harness was independently checked against the local tarball and its hash is recorded separately.
- Controller binary: `sha256:4167e7c9d7b871171e4e2130aac02a51b16fbe74d0cc1a40745c2885994aedd2`, Linux amd64, Ubuntu 26.04.1, kernel `7.0.0-34-generic`. It ran in a new `provision-mediation-controller` Incus VM cloned from the existing clean Host snapshot and assigned its own address and hostname.
- Trusted controller account: `authority`, UID/GID 1001. Requesters: `requestbot` 1002 and `requestother` 1003, with separate primary groups and mode-0700 homes. Only `authority` joined the requester groups. Neither requester had sudo or Host credentials.
- Registry, captured configuration, SQLite state and signing key: `/srv/mediation/private`, owned by `authority`, mode 0700; private files 0600. Inbox directories were `authority:REQUESTER` mode 2730; results directories were mode 2750. Their root-owned ancestors were 0755. The controller binary and harness were root-owned.
- Remote Host: `provision-acceptance.local` (`provision-acceptance`, Ubuntu 26.04, amd64), a separate Incus VM on the same `base.local` physical host/LAN. `mediation-host` had its own SSH credential and only the bootstrap's restricted executor sudo grant. The private authority key stayed on the controller; the public verifier was root-owned on the Host. The JSON records the executor digest, authority key identity, systemd/Caddy versions and pinned SSH host fingerprint.
- The root test supervisor provisioned accounts and exercised the explicit approval CLI as `authority` using `runuser` and a clean environment. This tests the independently authenticated operator entry point and requester confinement; it does not establish physical human presence, protect a compromised trusted operator/administrator, or add an automated approval policy.
- A controller-side HTTPS fixture supplied deterministic bytes and a delayed response. Its temporary CA was installed only in the disposable Host. Artifact digest: `sha256:72d5e9f4aa8568b3839437bdc305cf20f207bf069d5cae651be7ee31ce4573a9`. The small synthetic content proves staging and content identity, not installation or executable safety.

## Cases and observed results

| Case | Expected and observed result |
| --- | --- |
| Out-of-scope Environment | Capture attributed to `requestbot`, status `scope-or-registration-denied`; planning exited nonzero; complete Host inspection and cache were unchanged. |
| In scope, no mediated approval | A pre-existing Plan-wide approval did not authorize dispatch. No Host mutation; the request remained linked to its own exact Plan and operation. |
| Requester bypass | Reading signing/SSH keys and SQLite state, writing registry/state/configuration, impersonating `authority` through sudo or the approval command, signaling an operator process, modifying the binary, and reaching the executor without credentials all failed as expected. |
| Filesystem attacks | Actual requester-created symlink, hardlink and traversal submissions, and a foreign-owned inbox entry, were rejected by the real importer before planning. |
| Grant revoked after approval | Trusted registration was changed to remove the requester grant; execution failed before dispatch, with no cache mutation. Restoring the grant allowed the unchanged bound request to proceed. |
| Approved stage | Only `op-01`/`stageArtifact` ran. The independently observed Host `sha256sum`, approved Artifact digest, request-linked journal and published receipt agreed. |
| Modified original files | Changing the inbox submission to request installation and changing the original trusted YAML did not alter the private capture or approved work. Focused tests additionally rejected changed registered Revision inputs and duplicate/unknown authority fields. |
| Another operation / generic bypass | Generic execution of staging or `op-02` installation, and generic recovery, could not substitute the mediated approval. No installation, service activation or Endpoint mutation occurred. |
| Completed duplicate | Restoring and re-importing the original submission returned the same captured request and the recorded success; no second execution attempt was created. |
| Result access | `requestbot` read its own sanitized receipt; appending to it and reading `requestother`'s result failed. No private configuration, raw observation or Artifact source appeared in the receipt. |
| Approval expired before dispatch | New execution failed; read-only observation succeeded and status reported expiry. |
| Approval expired after dispatch | The HTTPS response was delayed 10 seconds under a 6-second approval. The dispatched bounded operation finished and its authoritative outcome was recorded after expiry. Signing lifetime remained capped by approval. |
| Known failure | A controlled digest mismatch produced `failed`. A retry with the same decision was refused without a new attempt; an explicit fresh operator decision permitted retry. |
| Uncertain outcome | The existing SSH fault shim discarded the successful response and hid the following observation. The journal recorded uncertainty; ordinary execution refused replay. `request resume` observed completed Host work and recorded success under a new fenced attempt with `resumeOfAttemptId`. |
| Expired uncertain recovery | A second response-loss case expired before recovery. Resume was refused, read-only observation remained available, and the attempt count stayed unchanged. |

The JSON retains the full before/after Host deployment state and listening ports. Both remained unchanged. Independent final `find` commands found no application release directories or application systemd units. Only authorized staging and explicit supervisor fault/reset actions changed the test Artifact cache.

## Reproduce and reset

Prerequisites are deliberately specific: administrative strict-SSH access to `base.local`, Incus with the disposable `provision-acceptance` VM and its `clean` snapshot, existing strict-SSH access as `marlinf@provision-acceptance.local`, Go 1.26, Python 3, OpenSSL, tar and scp. This exercise resets that VM and creates/deletes a separate controller clone. It does not touch `provision-backup-dest`.

From the repository root:

```sh
# Preserve earlier raw evidence before starting another run.
mv work/issue-130-live/evidence work/issue-130-live/evidence-previous
scripts/run-mediated-stage-acceptance.sh
```

Omit the `mv` on the first run. The wrapper builds controller/executor binaries, restores the clean Host snapshot, creates `provision-mediation-controller`, provisions the separate accounts and private directories, creates fresh authority/SSH keys, pins the Host key through the administrative Incus connection, installs the fixture CA and existing restricted bootstrap, then invokes:

```sh
# Inside the disposable controller, as the root test supervisor:
bash /opt/provision/scripts/test-remote-ssh-host.sh --mediated \
  --target provision-acceptance.local --target-user mediation-host \
  --controller-address CONTROLLER_IP --provision-version SOURCE_ID \
  --work-dir /srv/mediation/evidence \
  --confirm-disposable-host provision-acceptance
```

The version and IP come from the wrapper. The case driver uses the actual `request submit`, `import`, `plan`, `review`, `approve`, `execute`, `resume`, `observe` and `status` commands, and existing Plan/deployment commands for bypass tests. The accompanying JSON records their full argument vectors, expected result and observed exit code. Approval actors, fresh keys, request IDs, digests, attempt IDs and fencing tokens are generated during each run; compare their relationships and outcomes rather than expecting identical generated identities.

Success copies the evidence to `work/issue-130-live/evidence`, restores the Host's `clean` snapshot, and deletes the controller. Failures preserve both machines and copy available evidence for diagnosis. After inspecting a failed run, delete only the marked disposable controller, archive its local evidence, then rerun the wrapper; its first step restores the Host snapshot:

```sh
ssh base.local 'test "$(incus config get provision-mediation-controller user.provision-acceptance)" = issue130 && incus stop provision-mediation-controller --force && incus delete provision-mediation-controller'
```

## Limits

This proof covers one controller OS/account setup, one remote Host combination, and native HTTP `stageArtifact`. Both VMs share physical hardware and a LAN. It does not qualify other operations, local Hosts, other operating systems, hosted authorities, secret-bearing operations, installation/activation, or arbitrary application execution. The existing single-operator path remains a distinct trust boundary; its regression tests passed. This exercise did not rerun the full seven-scenario ordinary HTTP rollout matrix.
