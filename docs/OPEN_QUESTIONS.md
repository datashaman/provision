# Open questions

The product model is agreed. The following technical-architecture questions remain undecided.

## Technical architecture

- Curated host-local database, cache, queue, object-store, endpoint-router, container-runtime, and schedule implementations and supported versions?
- Host bootstrap, privilege, operating-system, filesystem, and isolation requirements?
- Concrete blue-green preparation, synchronization, routing, drain, rollback, and cleanup mechanics for each component role and target?
- Detailed AWS capability checks and unsupported combinations?
- Action sandboxing and credential-delegation mechanics on each execution target?
- Build execution, revision assembly, and multi-repository artifact-discovery mechanics?
- Configuration file organization, reference resolution, and generated-schema tooling?
- Concrete secret-store adapters and secret delivery mechanics?
- Upgrade compatibility and release-support policy for the Provision binary and installed runtime assets?
