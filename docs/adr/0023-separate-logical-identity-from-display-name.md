# Separate logical identity from display name

Applications, environments, and components have stable logical identities that survive display-name changes. Renaming does not imply infrastructure replacement unless an implementation exposes an unavoidable physical naming restriction, which must be shown in the plan. This prevents user-facing naming changes from accidentally becoming destructive lifecycle operations.
