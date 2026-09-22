# Version the application as an artifact manifest

An application revision is an immutable manifest identifying the complete set of component artifacts for one application version, even when those artifacts come from multiple source repositories. A new artifact creates a new complete revision. Individual artifacts may be reused and unchanged deployment work may be skipped, but deployment, promotion, and environment history always refer to the complete revision so partial changes cannot create an unrecorded mixture of application versions. Reapplying the same revision is valid.
