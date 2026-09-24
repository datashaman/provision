# Base host candidate preparation evidence — 2026-09-24

Issue #8 was exercised on the disposable `base.local` host from reviewed commit `accd75292729590cf5024cab42182d136fcddac0`. The host was freshly installed with Ubuntu Server 26.04.1 and resolved through Avahi to `192.168.101.130` during the run.

## Bootstrapped authority

The corrected bootstrap completed with no findings and reported `ready: true`:

- SSH ED25519 host fingerprint: `SHA256:Yfeob/OaXcVfb5AoEDNwRNNZz0klQxNGcd5Ow47mTEo`
- executor digest: `sha256:4726c873ea5ebf2dabb57c40713b3953c785004d65a79ec2f60ceb34b2a134bc`
- authority key ID: `sha256:bcd567b7450b54f7aced7e7a0517848791d852e06b17d37bf1a9d1e632a519a2`
- Environment account: `provision-lab`
- Environment root: `/var/lib/provision/environments/lab`, owned by `root:root` with mode `0755`
- enabled mutations: `stageArtifact`, `installGeneration`, `startCandidate`, and `verifyCandidate`

The root-owned Environment root is significant: an earlier live bootstrap attempt exposed that an account-owned parent contradicted the executor's immutable-storage check. Commits `03c1dde` and `accd752` corrected and centralized that invariant before any Artifact was staged on the final host.

## Deliberate pre-switch failure

The first approved Plan used a deliberately invalid candidate-verification path while retaining valid liveness and readiness checks:

- Plan: `sha256:974f845c8161d9b9f7f8ea068e001f7c6c42d378f9f2a57fb279aaf71f5e92fd`
- configuration digest: `sha256:2d351894ea1e5a4ecfa30c3299206bb219421681654004ee29ea578b57ffda14`
- Artifact: `sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5`
- Generation: `provision-example-http-v1-bac304a88517`
- unit: `provision-lab-web-bac304a88517.service`
- private candidate port: `27811`

The append-only journal recorded:

| Operation | Attempt | Fence | Outcome |
| --- | --- | ---: | --- |
| `op-01` stage Artifact | `attempt-f5aa1f22616d1bc4496da5ef86031bf8` | 1 | succeeded, staged 4,781,926 bytes |
| `op-02` install Generation | `attempt-f69351b69c8c9befa071d644386dffae` | 2 | succeeded |
| `op-03` start candidate | `attempt-debef28e72a53e3074c9dbb7e0b68d67` | 3 | succeeded |
| `op-04` verify candidate | `attempt-cd2bf7afbc1b7e9fc7ac380d5d58ee98` | 4 | failed as planned |

The failed health result recorded liveness `204` and readiness `204`, then rejected candidate verification because it did not report the planned Revision. It reported `switchEligible: false` and `candidateCleaned: true`.

Independent observations after failure confirmed:

- the candidate unit was absent;
- the candidate release directory was absent;
- the active-generation record remained absent;
- the stable endpoint on port `18080` remained inactive;
- host inspection showed no candidate port and no active deployment.

## Successful candidate

The correct configuration was previewed into the same Environment State Backend so fencing remained monotonic, then approved and executed:

- Plan: `sha256:dc7fbb1afe5fae49c582a52efbce73a80b03f301ac49ae43c6aca3984b2c487b`
- configuration digest: `sha256:65d86e70fd44804bf475af98f3394d009baabf6ceaef9d3c74b91441d21f1a78`
- observation digest: `sha256:00e977e1ddd6bdfa27027837ad3b54fe87e97b0490242b92465cbf0f63a4b70a`

| Operation | Attempt | Fence | Outcome |
| --- | --- | ---: | --- |
| `op-01` stage Artifact | `attempt-296dfeb8297de7f82aee5c23494404e0` | 5 | succeeded, already present and digest-verified |
| `op-02` install Generation | `attempt-30257369ef32a46f14a15b134442548b` | 6 | succeeded |
| `op-03` start candidate | `attempt-63b4f201a5571a37463fc7738f1312f4` | 7 | succeeded |
| `op-04` verify candidate | `attempt-5c9df363bf0cc6ff6b04883915248b6a` | 8 | succeeded, switch-eligible |

The final typed health observation reported:

- liveness `/live`: `204`, healthy;
- readiness `/ready`: `204`, healthy;
- candidate verification `/verify`: `200`, healthy;
- reported Revision: `provision-example-http-v1`;
- `candidateActive: true`;
- `switchEligible: true`.

Independent systemd and HTTP observations agreed with the signed result:

- unit state: `active`;
- `User=provision-lab`;
- `WorkingDirectory=/var/lib/provision/environments/lab/releases/provision-example-http-v1-bac304a88517`;
- `PROVISION_HTTP_LISTEN=127.0.0.1:27811`;
- `PROVISION_REVISION=provision-example-http-v1`;
- executable SHA-256: `46d89353fe1eefbe2b35e9bb24be6668be1e33983dda053f57cef2f86d2d6b69`;
- immutable `.provision-generation.json`: root-owned mode `0444`, binding the Generation, Revision, Artifact digest, runtime account, release directory, and executable.

Host inspection observed candidate port `27811` but still reported no active deployment. Port `18080` remained inactive and `/var/lib/provision/environments/lab/active-generation.json` remained absent. Candidate preparation therefore produced health-gated switch eligibility without changing the stable Endpoint.

## Fencing side observation

A separate throwaway State Backend restarted its local fence at 1 and attempted the successful Plan once. The host rejected that signed operation as stale before mutation because fence 4 had already been consumed. The throwaway backend recorded the response as uncertain. The valid run then used the original Environment State Backend and continued at fence 5. This confirms the host's stale-writer protection and also demonstrates why one Environment must keep one authoritative fencing lineage.

## Boundary

This evidence covers only the implemented pre-switch slice. `switchEndpoint`, switched-route verification, drain, and rollback-window retention remain unavailable and were not simulated manually.
