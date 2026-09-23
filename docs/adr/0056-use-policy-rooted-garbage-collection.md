# Use policy-rooted garbage collection

State retention protects everything reachable from live environment heads, approvals, resumable operations, rollback windows, retained generations, audit holds, and recovery records. Snapshots accelerate reads without rewriting journal history, while unreferenced immutable objects pass through mark, quarantine, and delayed deletion. This makes cleanup bounded and auditable without allowing storage pressure to erase recovery evidence silently.
