# Clone environment intent rather than state

Cloning an environment creates a new identity from the source configuration intent, then resolves its own secret references, implementations, domains, and policies. Stateful data is not copied implicitly and moves only through an explicit data refresh or restore operation. This makes cloning safe and reviewable instead of disguising data movement or credential reuse as configuration convenience.
