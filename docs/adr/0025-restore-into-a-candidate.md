# Restore into a candidate

A restore creates a candidate store generation or separate recovery environment, verifies it, and requires an explicit cutover before it becomes authoritative. Destructive in-place restore is available only through exceptional approved policy. This favors recoverability and inspection over convenience when damaged, stale, or incompatible restored data could otherwise overwrite the active store.
