# Use journaled state behind a State Backend

Each environment stores an append-only execution journal plus immutable snapshots for efficient reads. A deep State Backend module exposes conditional journal append, snapshot load and compare-and-swap, immutable Plan and evidence storage, and fenced lease operations. SQLite and AWS-backed adapters make the seam real while keeping persistence details out of the Planner and Executor.
