# Pin runtime assets per environment

The Provision binary advertises compatibility with persisted formats, operation kinds, and installed runtime assets, and reads the previous major persisted format during a documented migration window. Each environment pins its runtime applets, which change through an auditable Plan. This lets an operator upgrade the CLI without unexpectedly changing the schedule or execution behavior of deployed workloads.
