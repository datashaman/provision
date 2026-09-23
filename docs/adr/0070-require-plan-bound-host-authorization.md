# Require Plan-bound host authorization

A remote host must not treat an authenticated SSH connection as permission to deploy arbitrary content. Its restricted executor verifies a short-lived authorization bound to the exact approved Plan, target environment, and host before accepting typed operations; this adds a trust boundary even when the initiating CLI's state is local and inaccessible to the host. The added authorization and key-management work is deliberate because SSH authentication alone cannot prove that an operation was planned and approved.
