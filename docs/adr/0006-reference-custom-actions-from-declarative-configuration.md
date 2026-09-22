# Reference custom actions from declarative configuration

Builds, migrations, verification, and lifecycle work may contain unrestricted application code, but configuration references that code as bounded actions rather than executing general-purpose code while configuration is loaded. Each action declares its phase and execution contract, preserving inspectable plans without pretending application-specific work can be reduced to built-in primitives.

