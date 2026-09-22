# Use one authoritative implementation per component and environment

Each component resolves to exactly one authoritative implementation in an environment. Blue-green deployment uses two revisions of that implementation, while migration may temporarily involve a source and destination; this invariant avoids ambiguous ownership, behavior, and routing while still permitting controlled implementation changes.

