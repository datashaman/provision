# Open questions

The product model is agreed. The following technical-architecture questions remain undecided.

## Technical architecture

- Exact host executor operation allowlist, privilege isolation, filesystem layout, and SSH transport hardening?
- Detailed host router behavior for HTTP and WebSocket draining, rollback, and long-lived connections?
- Detailed store cutover algorithms for PostgreSQL, Valkey, filesystem objects, RDS, ElastiCache, and S3, including failure injection?
- Queue migration protocol details for RabbitMQ and SQS, including producer fencing and transfer verification?
- Whether any Lambda-oriented WebSocket implementation can pin connections to handler generations and satisfy required blue-green?
- Detailed AWS capability checks and unsupported combinations, including regional service features and quotas?
- Action isolation and credential-delegation mechanics on each execution target?
- Secret version observation and revocation race handling?
- Build artifact publication, reproducibility evidence, and native-build exception mechanics?
- Exact certified product versions and automated compatibility-test matrix for each release?
- Backup formats, destination credentials, and automated restore-verification mechanics?
