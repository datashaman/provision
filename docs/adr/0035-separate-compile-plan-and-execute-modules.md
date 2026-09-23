# Separate compile, plan, and execute modules

The architecture centers on three deep modules with small interfaces: the Configuration Compiler produces a validated canonical model with provenance; the Planner converts desired model, observed state, and capability contracts into an immutable Plan; and the Executor applies an approved Plan through implementation adapters and an execution journal. CLI, future coordination, automation, and tests use these same interfaces, concentrating behavior instead of duplicating it across entry points.
