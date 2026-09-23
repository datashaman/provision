# Version every persisted format

Every persisted format carries an explicit schema version. Configuration compiles forward into the current model, snapshots migrate deterministically, journal events remain immutable within a declared reader compatibility window, and approved Plans are never rewritten. A Plan outside the executing engine's compatibility range must be replanned, preserving the meaning of approvals and audit evidence rather than silently translating them.
