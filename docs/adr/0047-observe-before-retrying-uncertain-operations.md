# Observe before retrying uncertain operations

Every operation has deterministic identity, preconditions, retry safety, an observation method, verification, and optional recovery instructions. The Executor journals intent before side effects and outcome afterward. If execution becomes uncertain, it observes provider state before retrying and uses deterministic provider idempotency tokens where supported; uncertain single-attempt work pauses for explicit recovery. This avoids both blind duplication and falsely assuming an interrupted call failed.
