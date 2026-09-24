# Disposable Ubuntu host bootstrap

This is the explicit, privileged preparation step for the first native HTTP tracer. It is not a Deployment or a substitute for a Plan. It uses a small audited shell script rather than Ansible; no runtime component depends on Ansible. The script currently supports Ubuntu only. It refuses to run if `/srv/gimme` or a Gimme systemd unit is present.

Bootstrap creates a non-login `provision-<environment>` account and a private Environment directory, installs a root-owned nonresident executor, and gives one existing operator account passwordless `sudo` access to that executable alone. The executor presently supports inspection only. All deployment operations remain disabled until Plan-bound authorization is implemented and tested in [issue #6](https://github.com/datashaman/provision/issues/6). An operator's other host privileges, if any, are outside this restricted path.

## Before touching a host

Use a supplied disposable Ubuntu machine that has been reset outside Provision, or obtain a separate explicit ownership decision for any existing workloads. Reimaging is not performed by Provision. Never apply this script over a Gimme-managed host: the script refuses `/srv/gimme` and Gimme systemd units rather than assuming ownership. Record the machine identity and SSH host-key fingerprint out of band after reimaging. The remote CLI uses strict host-key checking and will not silently trust a changed key. The first reset-host run is recorded in [the 2026-09-24 lab evidence](evidence/2026-09-24-base-host-bootstrap.md).

The bootstrap operator must already exist on the host and must be able to run the one-time script as root. After bootstrap, ordinary Provision inspection uses only the restricted sudo rule. Caddy is installed from the host's configured Ubuntu package sources if missing, and its systemd service is enabled. No Caddy routing is configured by this step.

## Build, preview, and apply

From a fresh clone on the operator machine, build the Linux executor for the target architecture:

```sh
mkdir -p examples/host-http/dist
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -o examples/host-http/dist/provision-host-executor ./cmd/provision-host-executor
```

Use `GOARCH=arm64` for an arm64 target. Copy the executable and `scripts/bootstrap-host.sh` to an operator-controlled temporary directory on the reset host. On that host, first preview, then apply deliberately:

```sh
bash bootstrap-host.sh --dry-run --environment lab --operator OPERATOR --binary ./provision-host-executor
sudo bash bootstrap-host.sh --apply --environment lab --operator OPERATOR --binary ./provision-host-executor
```

The apply step checks for Gimme, conflicting paths, mismatched account identity, an existing different executor, and different bootstrap or sudoers records before installation. Re-running with the same inputs is intended to be safe; drift is reported rather than silently overwritten. It emits an inspection result at the end. Keep the build inputs and printed executor digest with the host's test evidence.

From the operator machine, verify the installation through the public CLI and an already trusted SSH host key:

```sh
go run ./cmd/provision host inspect --address HOST --user OPERATOR
go run ./cmd/provision host bootstrap check --address HOST --user OPERATOR --environment lab --operator OPERATOR
```

The second command reports the OS and architecture, systemd, OpenSSH-server and Caddy versions, Caddy and journald service state, cgroup-v2 observation, executor digest, dedicated account, available operations, and drift findings. `ready: true` means only that bootstrap matches the declared setup; it does **not** certify blue-green deployment. Support and required-mode guarantees need the later host scenario tests and published matrix.

## Recovery and reset

If bootstrap fails after some preparation, read its error and the `host bootstrap check` findings before retrying. Do not manually replace a conflicting executor or sudoers rule just to make a check pass. For the disposable lab, the clean recovery path is to reimage the exact host through the owner's reset mechanism and repeat from the preview. Provision does not delete the foundational host or perform an automated uninstall.

The bootstrap uses Ubuntu's system-account facilities and Caddy's packaged systemd service. See the [Ubuntu user-management documentation](https://ubuntu.com/server/docs/how-to/security/user-management/) and [Caddy installation documentation](https://caddyserver.com/docs/install) for the distribution-level behavior.
