# Treat execution targets as external foundations

An environment may place components across multiple named execution targets, but the referenced hosts, cloud accounts, clusters, and serverless services remain external foundational infrastructure. Provision validates access and capabilities and manages only application-scoped resources placed there. This enables hybrid environments and shared foundations without allowing environment destruction to imply ownership or deletion of the underlying targets.
