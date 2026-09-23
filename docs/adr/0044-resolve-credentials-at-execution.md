# Resolve credentials at execution

The executing engine or runner resolves AWS, SSH, secret-store, and Action credentials at execution time. Configuration and Plans contain references rather than values; runners use local profiles, IAM Identity Center, assumed or workload roles, operating-system SSH agents, or referenced key material with strict host verification. Actions receive narrowly scoped temporary credentials and secrets for their lifetime only. Resolved values never enter Plans, journals, or evidence.
