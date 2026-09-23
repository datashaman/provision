# Represent plans as canonical data

Plans are deterministic serializable data containing typed operations, dependencies, preconditions, approvals, expected observations, sensitive-value references, and recovery behavior. Custom Actions are referenced by digest and contract rather than embedded as closures or generated scripts. This makes plans reviewable, hashable, persistable, transferable to a runner, and safely rejectable when their inputs become stale.
