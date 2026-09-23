# Use DynamoDB and S3 for shared state

The AWS State Backend stores environment heads, journal metadata, leases, fencing tokens, and conditional transitions in DynamoDB, while S3 stores content-addressed Plans, snapshots, evidence, and larger immutable records. Both use KMS encryption. DynamoDB transactions condition commits on current state and fencing tokens, and state heads reference immutable S3 digests. This separates concurrency-sensitive metadata from durable immutable payloads without introducing a mandatory hosted Provision service.
