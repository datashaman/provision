# Version environment configuration separately

Every deployment records an immutable application revision together with an immutable environment configuration revision containing resolved behavior values, feature flags, implementation selections, and policy references. Keeping these identities separate allows an application revision to move unchanged between environments while still making environment-specific changes reproducible and attributable.
