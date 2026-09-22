# Preserve portable intent rather than promise identical behavior

Provision will preserve the declared intent of an application component across environments while exposing meaningful differences between implementations. It will validate requirements and reject an implementation that cannot satisfy them instead of claiming that systemd, ECS, Lambda, and managed services behave identically; this keeps portability honest without reducing every environment to the smallest common denominator.

