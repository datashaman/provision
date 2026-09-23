# Open questions

The product model is agreed. The following technical-architecture questions remain undecided.

## Technical architecture

- Exact host executor operation allowlist, Plan-authorization proof encoding and key management, replay protection, privilege isolation, filesystem layout, and SSH transport hardening?
- Whether a bounded Ansible host-stack adapter provides enough leverage to justify a second host-preparation implementation, or whether the curated host executor should own the initial path?
- Whether mise is supported as an optional exact-version native-host runtime, in addition to developer and isolated-build tooling, and how its versions enter Plan evidence?
- Detailed host router behavior for HTTP and WebSocket draining, rollback, reconnect signalling, and deadline enforcement?
- Detailed store cutover algorithms for PostgreSQL, Valkey, filesystem objects, RDS, ElastiCache, and S3, including failure injection?
- Queue migration protocol details for RabbitMQ and SQS, including producer fencing, transfer verification, and proof of any declared ordering guarantee?
- Whether any Lambda-oriented WebSocket implementation can pin connections to handler generations and satisfy required blue-green?
- Detailed AWS capability checks and unsupported combinations, including regional service features and quotas?
- Action isolation and credential-delegation mechanics on each execution target?
- Non-disclosing change detection for unversioned local secret sources and revocation race handling?
- Build artifact publication, destination digest verification, reproducibility evidence, and native-build exception mechanics?
- Exact certified product versions and automated compatibility-test matrix for each release?
- Adapter-owned backup formats, destination credentials, application-consistency Action boundaries, and automated restore-verification mechanics?

## Potential product-scope changes

These are not accepted first-release requirements. The agreed product model treats execution targets as external and does not imply Gimme state import.

- Should a later Provision lifecycle create and destroy EC2 instances or other VMs, rather than only bootstrap and deploy to supplied hosts?
- Should Provision support a verified in-place migration of Gimme-owned deployments, or should existing Gimme installations remain independent while new Provision environments are created?
