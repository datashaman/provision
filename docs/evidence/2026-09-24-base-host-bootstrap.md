# Reset `base` host bootstrap evidence — 2026-09-24

This record covers the first live run of the restricted Host Target bootstrap from issue #5. It qualifies only the bootstrap and inspection path; it does not certify deployment or blue-green behavior.

## Supplied starting state

- Owner explicitly confirmed that the whole host was disposable and reset it to the latest available Ubuntu Server.
- SSH target during the run: `marlinf@192.168.101.109`; hostname: `base`.
- The reset host did not yet advertise `base.local`, so the run deliberately used the supplied address.
- Pinned ED25519 host-key fingerprint: `SHA256:25F8cX/ZCKLj2FExdbuMWV7W1JSZuB7KTDamEcPaAJ0`.
- `/srv` was empty, no `gimme-*` systemd units existed, and `/etc/provision` and the Provision executor were absent.
- Caddy and Podman were absent. Podman is not required by this native bootstrap increment.

## Applied bootstrap

The amd64 Linux executor was built with `CGO_ENABLED=0`, copied with `scripts/bootstrap-host.sh`, and applied twice with identical inputs for environment `lab` and operator `marlinf`. Both applies completed successfully. The installed executor digest was:

```text
sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5
```

The resulting bootstrap record named account `provision-lab` and matched that digest. Caddy was installed from the host's configured Ubuntu package sources.

## External verification

`provision host bootstrap check` returned `ready: true` with no findings and observed:

| Capability | Observation |
| --- | --- |
| OS | Ubuntu 26.04, amd64 (`x86_64`) |
| systemd | `systemd 259 (259.5-0ubuntu3.4)` |
| SSH server | `OpenSSH_10.2p1 Ubuntu-2ubuntu3.6, OpenSSL 3.5.5 27 Jan 2026` |
| Caddy | `2.6.2`, active |
| journald | active |
| cgroup v2 | observed |
| executor operations | `inspect` only |

Direct negative checks confirmed that passwordless `sudo /bin/true` was denied and that an unsupported executor `shell` operation was denied. Restricted passwordless executor inspection succeeded. After both applies, `/srv` remained empty and no `gimme-*` unit existed.

This evidence proves repeatable preparation, capability inspection, and the current command boundary on this supplied host. It does not prove a workload deployment, Caddy route switch, rollback, or any required blue-green guarantee.
