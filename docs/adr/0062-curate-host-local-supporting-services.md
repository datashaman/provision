# Curate host-local supporting services

The managed host portfolio uses PostgreSQL, Valkey, RabbitMQ quorum queues, a filesystem-backed object store, and Caddy. These choices let a host-only environment provide all required first-class roles without AWS. Each implementation still declares narrow, observed capabilities: replication limits, queue ordering and deduplication, filesystem rebinding, and connection drain may prevent a particular required blue-green transition.
