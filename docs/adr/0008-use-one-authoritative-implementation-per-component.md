# Use one authoritative implementation per component and environment

Each component resolves to exactly one authoritative implementation in an environment. Blue-green deployment uses two application revisions or, for a store, two physical generations of that implementation. A store transition or controlled implementation migration may temporarily involve active and candidate realizations but must finish with one authoritative binding and generation. This invariant avoids ambiguous ownership, behavior, and routing while still permitting controlled changes.
