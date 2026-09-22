# Keep interactive access outside core operations

Provision does not expose general-purpose remote shell execution as a core operation. Repeatable work uses Tasks or bounded Actions with plans and audit history, while emergency interactive access remains an external break-glass mechanism. This prevents routine operations from bypassing declared permissions, reproducibility, and safety controls without pretending emergencies never require direct access.
