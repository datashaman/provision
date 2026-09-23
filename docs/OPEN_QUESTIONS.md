# Open questions

The product model is agreed. The following technical-architecture questions remain undecided.

## Technical architecture

- Exact host executor operation allowlist, privilege isolation, filesystem layout, and SSH transport details?
- Concrete host blue-green preparation, synchronization, routing, drain, rollback, and cleanup mechanics for each component role?
- Concrete AWS blue-green preparation, synchronization, routing, drain, rollback, and cleanup mechanics for each component role?
- Detailed AWS capability checks and unsupported combinations, including regional service features and quotas?
- Action isolation and credential-delegation mechanics on each execution target?
- Secret rotation, revocation, and refresh behavior during runtime and rollout?
- Build execution environment, reproducibility evidence, and artifact publication mechanics?
- Exact certified product-version policy and automated compatibility-test matrix?
- Host-local backup destinations and restore-verification mechanics without AWS dependencies?
- Should the first-class `cache` role be named `key-value store`, and should its Store Data Role always be explicit instead of defaulting to `derived`?
