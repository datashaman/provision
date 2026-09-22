# Bind plans to observed state

A plan is immutable and bound to exact application and environment configuration revisions, observed state, selected capabilities, and artifact digests. Any relevant change makes it stale and requires replanning; destructive work and safety fallbacks require approval against the current plan. This prevents a previously reviewed plan from authorizing materially different work after the environment or inputs have changed.
