# Authorized SSH preparation on reset `base` host — 2026-09-24

This record covers the final live remote-host proof for issue #7 using an executor built from source commit `3ebca93ec66a89d879d092449d526863fe57d590`. It proves only the approved `stageArtifact` preparation operation. It does not claim that a Generation was installed or that a blue-green route switch occurred.

## Isolated starting state

- The owner reset the supplied disposable host to Ubuntu Server before this run.
- SSH target: `marlinf@192.168.101.109`; hostname: `base`.
- The reset changed the host key. Strict SSH verification rejected the stale identity before any bootstrap or execution command ran.
- The owner verified the replacement ED25519 fingerprint out of band: `SHA256:2bgg3rx2GkMvsJxbDKK9SEAox2v8oXpWgiDI/JbkWj0`.
- After pinning that key, read-only checks found no `/srv/gimme`, no `gimme-*` systemd unit, and no existing Provision executor.

## Restricted bootstrap

The amd64 executor was built with `CGO_ENABLED=0`. Its public authorization key and the audited bootstrap script were copied to an operator-owned temporary directory. The bootstrap dry run made no changes; the owner then crossed the explicit interactive-sudo boundary to apply it.

The resulting external bootstrap check returned `ready: true` with no findings and observed:

| Identity or capability | Observation |
| --- | --- |
| OS | Ubuntu 26.04, amd64 (`x86_64`) |
| systemd | `systemd 259 (259.5-0ubuntu3.4)` |
| SSH server | `OpenSSH_10.2p1 Ubuntu-2ubuntu3.6` |
| Caddy | `2.6.2`, active and valid |
| executor digest | `sha256:ebf943caa9c6db8b04c194bac822713a8fc82530755d9f386fe6395147669a59` |
| authority key ID | `sha256:848e71d781035b04bf9f098fcd4087563a60731796f7404afe7ef727add64e73` |
| SSH host-key fingerprint | `SHA256:2bgg3rx2GkMvsJxbDKK9SEAox2v8oXpWgiDI/JbkWj0` |
| allowed operations | `inspect`, `stageArtifact` |

Only the public verification key was installed on the host. The private signing key remained on the operator machine.

## Plan-bound remote execution

The example configuration was copied outside the repository and changed only to address the reset host by its supplied IP. Preview persisted Plan `sha256:3a8d349bd36fb78a94e8cf052fc899f2df3db5b115d644825fb8af496bebbed0`. The Plan recorded the executor digest, authority identity, and confirmed SSH host-key fingerprint shown above. Approval re-observed the host, matched the same Plan, and recorded actor `marlinf` with a 15-minute expiry.

Execution of `op-01` then:

1. acquired and journaled fenced attempt `attempt-23c6c85ca897b0e8e19ea56f007487e1` with fencing token `1`;
2. observed the remote digest cache through the fixed executor;
3. sent the signed typed `stageArtifact` envelope over strict, non-interactive SSH;
4. downloaded and verified the declared Artifact on the host; and
5. committed the structured successful outcome to the State Backend.

The execution result reported `status: staged`. A separate post-run executor observation then reported `status: already-present`. Both agreed on the immutable identity and size:

```text
path: /var/lib/provision/artifacts/sha256/bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5
digest: sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5
size: 4781926
```

A fresh CLI process read back exactly one authoritative intent and one authoritative `succeeded` outcome for that attempt. There were no rejected results. Final isolation checks still found no `/srv/gimme` and no `gimme-*` systemd unit.

## Failure behavior

The live reset demonstrated that a changed host identity fails closed under strict host-key checking. Automated acceptance coverage also forces a connection loss after dispatch and a failed post-loss observation. That path records an authoritative `uncertain` outcome with recovery mode `discard-staged`; it does not assume success, infer failure, or blindly retry the mutation.

This evidence establishes transport parity for the narrow preparation operation: local and SSH execution use the same typed input, approval decision, short-lived Plan authorization, fenced attempt, executor checks, structured result, and durable journal semantics. The live Plan bound the confirmed ED25519 fingerprint, and the final executor recomputed and matched that fingerprint before accepting the signed envelope.
