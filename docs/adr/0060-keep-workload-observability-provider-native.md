# Keep workload observability provider-native

Provision emits structured management events and records evidence references, while host workloads use journald and AWS workloads use CloudWatch by default. Health gates consume explicit probes and selected metrics, and optional OpenTelemetry export may integrate other systems. Provision does not become a required telemetry backend, preserving the rule that deployed workloads continue operating when management is unavailable.
