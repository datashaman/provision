# Bind plans to observed state

A plan is immutable and bound to exact application and environment configuration revisions, observed state, selected capabilities, and artifact digests. Any relevant change makes it stale and requires replanning; destructive work and safety fallbacks require approval against the current plan. This prevents a previously reviewed plan from authorizing materially different work after the environment or inputs have changed.

A changed preview may replace the current plan only when the environment has no active execution lease. An unexpired, unreleased lease preserves the running plan's authority to renew and record its outcome; persisting a different preview is refused until the lease is released or expires. Planning and read-only previews remain concurrent under ADR 0039. This defers supersession without relaxing state revalidation for new execution.
