# Base host Endpoint switch evidence — 2026-09-25

Issue #9 was exercised from merged commit `5fe8c666526bd7a15e4839a1a7ade71e24397e43` on a freshly reset, disposable `base.local` host. The host ran Ubuntu Server 26.04 at `192.168.101.133` during the final run.

## Bootstrapped authority

Bootstrap inspection reported `ready: true` with no findings:

- SSH ED25519 host fingerprint: `SHA256:C2cRkpSljxS5eFa6B67SerdtfZwM42iQgnhX+1gCh90`
- executor digest: `sha256:ac56ff7a9236be185d75b29997d1089d67c52c8e3ebea1a3b44efe8e9d2cfbaf`
- authority key ID: `sha256:bcd567b7450b54f7aced7e7a0517848791d852e06b17d37bf1a9d1e632a519a2`
- Caddy: `2.6.2`, active, valid admin configuration, and reachable admin API
- durable Caddy service contract: `/usr/bin/caddy run --environ --resume`
- enabled operations: `stageArtifact`, `installGeneration`, `startCandidate`, `verifyCandidate`, and `switchEndpoint`

## First activation

The first Plan activated the `v0.1.0` fixture Artifact on the previously unused stable port `18080`:

- Plan: `sha256:944f4247115e8c265a8ecba88a0e4300f6b78b21f5ed4cb01d967911289451d2`
- Artifact: `sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5`
- Generation: `provision-example-http-v1-bac304a88517`
- private port: `27811`

| Operation | Attempt | Fence | Outcome |
| --- | --- | ---: | --- |
| `op-01` stage Artifact | `attempt-75b59b623a78b61981832e63a74b365e` | 1 | succeeded |
| `op-02` install Generation | `attempt-6d273e495b7d81b6957893cafb1150ee` | 2 | succeeded |
| `op-03` start candidate | `attempt-1aa4b28a59142cc999829ae42be1479f` | 3 | succeeded |
| `op-04` verify candidate | `attempt-f59b076ef19840e9c3f3efd5ac68178d` | 4 | succeeded, switch-eligible |
| `op-05` switch Endpoint | `attempt-8329c2372659cb13966edc5d5b7488f2` | 5 | succeeded, active |

The stable Endpoint returned `{"revision":"provision-example-http-v1"}` after activation.

## Retained previous Generation

The second Plan switched to the delayed-capable `v0.2.0` fixture and retained the first Generation:

- Plan: `sha256:cc52845525ea4f46e1a7d90ac82fcb83beec6c98df4bb226a6121628c3b40416`
- active Generation: `provision-example-http-v2-f5d67ce429e0` on private port `22934`
- previous Generation: `provision-example-http-v1-bac304a88517` on private port `27811`
- fences: 6 through 10, with all five operations succeeding

The typed switch result reported `candidateVerified: true`, `gracefulReload: true`, and `previousRetained: true`. The stable Endpoint returned `{"revision":"provision-example-http-v2"}` while both systemd units remained active.

## In-flight HTTP request drain

A third Plan used the distinct `v0.2.1` fixture Artifact so both the active and candidate Generations exposed the same bounded delayed-response contract:

- Plan: `sha256:8a385a032eb61f52066fea2fb6a32bdd68934713430b7aaa323d9850dfd1dc73`
- Artifact: `sha256:4e775436b605b9e7ea71c1bdc0941e3f5e345eab05ae264f6cf9fbe56afd6485`
- candidate Generation: `provision-example-http-v3-4e775436b605` on private port `20087`
- planned previous Generation: `provision-example-http-v2-f5d67ce429e0` on private port `22934`

| Operation | Attempt | Fence | Outcome |
| --- | --- | ---: | --- |
| `op-01` stage Artifact | `attempt-9cefa3c0648f396d996bb4711c2687f0` | 11 | succeeded |
| `op-02` install Generation | `attempt-2d95b6f661b5f3ce278d73cef6c6d115` | 12 | succeeded |
| `op-03` start candidate | `attempt-5743c5e1afc672a14a1a0936512eba5b` | 13 | succeeded |
| `op-04` verify candidate | `attempt-d92a653b5b687ee0a128b11c7b215c32` | 14 | succeeded, switch-eligible |
| `op-05` switch Endpoint | `attempt-616cebc382c7debe3065c987bcd64d5c` | 15 | succeeded, active |

Before `op-05`, a ten-second request was opened through stable port `18080` and accepted by `v2`. During that request, `op-05` completed and a new stable request immediately returned `{"revision":"provision-example-http-v3"}`. The original request then completed successfully with `{"revision":"provision-example-http-v2"}`. This directly exercises ordinary in-flight HTTP request preservation rather than inferring it from Caddy's reload response.

After the switch:

- both `v2` and `v3` systemd units were active;
- their private verification Endpoints returned their respective Revisions;
- inspection reported `v3` as active and `v2` as previous;
- the retained `v2` release directory was root-owned mode `0755`, its manifest root-owned mode `0444`, and its executable root-owned mode `0555`;
- the append-only journal recorded all five third-Plan outcomes as successful and preserved both Generation identities in the switch result.

## Caddy restart durability

Caddy was restarted as a separate process after the switch. Its service re-entered active state at `2026-09-25 03:09:14 SAST` using `caddy run --environ --resume`. After restart:

- stable port `18080` still returned `{"revision":"provision-example-http-v3"}`;
- the admin API remained reachable and valid;
- inspection still reported the route upstream as `127.0.0.1:20087`;
- `v3` remained active and route-matched;
- `v2` remained active as the retained previous Generation;
- bootstrap inspection remained `ready: true` with no findings.

## Live defects caught before the final run

Earlier reset-host attempts failed closed without changing stable traffic and exposed three integration gaps that unit-level fakes had missed:

1. the State Backend did not yet allow the planned `switchEndpoint` operation to begin;
2. a healthy preflight short-circuited signed candidate verification, leaving the host without the durable exact verification record required for switching;
3. Caddy 2.6.2 returned `200 null` for an absent JSON configuration path, while the fake controller represented absence as `404`.

The final run includes the merged corrections and regression tests for all three cases. A separate merge cleanup removed a duplicated switch case before the final executor was built.

## Boundary

This evidence qualifies the implemented ordinary-HTTP activation slice: exact signed candidate verification, atomic stable-route replacement, preservation of an in-flight HTTP request, retained previous Generation, durable active/previous inspection and journal evidence, and Caddy process-restart survival.

It does not qualify WebSocket or other long-lived stream draining, post-switch stable-route health verification, automated rollback, drain-completion declaration, rollback-window cleanup, or full host-reboot survival. The earlier `v1` Generation also remains runnable because retention cleanup is not implemented yet.
