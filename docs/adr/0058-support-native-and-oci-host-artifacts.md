# Support native and OCI host artifacts

The systemd host implementation accepts both digest-addressed executable bundles and OCI images. Native bundles run in hardened systemd units, while images use a declared container runtime whose lifecycle is still managed by systemd. Supporting both avoids making containers mandatory for local development without excluding the dominant immutable format for packaged web workloads.
