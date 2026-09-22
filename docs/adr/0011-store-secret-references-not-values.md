# Store secret references rather than secret values

Committed configuration records secret references, never production secret values. Each environment's implementation resolves those references from an appropriate source; local environments may use process environment variables, ignored files, or a local secret provider. Plans, history, and diagnostics must preserve the reference while preventing resolved values from being disclosed.
