# Use rootless Podman and Quadlet on hosts

The curated OCI host path uses rootless Podman and Quadlet under a dedicated environment account, with systemd user units and lingering for boot persistence. Native bundles continue to use hardened systemd units. This keeps the host lifecycle in systemd and avoids requiring a privileged container daemon while preserving OCI support; other runtimes require a separate adapter and capability contract.
