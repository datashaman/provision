# Use a shared table and bucket per installation

An administrative Provision installation uses one DynamoDB table and one S3 bucket, partitioning DynamoDB records by environment identity and addressing immutable S3 objects by digest. Transactional event append and head advancement remain in DynamoDB, while large Plans, snapshots, and evidence remain in S3. This avoids per-environment infrastructure sprawl while preserving isolation through keys, conditions, encryption, and access policy.
