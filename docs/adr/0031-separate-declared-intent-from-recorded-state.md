# Separate declared intent from recorded state

Declarative configuration is authoritative for intended application and environment behavior, while Provision's recorded state is authoritative for observed identities, ownership, active revisions, operation history, and recovery progress. Neither silently overwrites the other; differences become drift requiring a current plan. This keeps configuration reviewable without pretending operational history and provider identities can be reconstructed safely from configuration alone.
