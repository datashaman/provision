# Restrict host operations to typed bundles

Host bootstrap installs a root-owned, nonresident executor and dedicated environment accounts. Later deployment work passes typed operation bundles to its restricted entrypoint rather than granting the invoking user arbitrary sudo or shell access. Host certification observes systemd, cgroup v2, rootless Podman and Quadlet, journald, filesystem, and networking capabilities, beginning with Ubuntu LTS and Amazon Linux 2023 on amd64 and arm64.
