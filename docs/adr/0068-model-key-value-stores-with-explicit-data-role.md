# Model key-value stores with an explicit data role

The first-class role formerly called `cache` is named `key-value store`, and every instance must declare whether its data is authoritative, derived, or ephemeral. Valkey, Redis, and ElastiCache can hold sessions and other application state as well as rebuildable cache entries; a rebuildable default would therefore authorize data loss through a misleading component name. The selected data role controls recovery and store-transition validation without assuming that the implementation determines the importance of its contents.
