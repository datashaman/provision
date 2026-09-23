# Fence environment mutations

One mutating operation may execute per environment under a renewable lease with a monotonically increasing fencing token. Every conditional journal append verifies that token, so an expired or partitioned executor cannot commit after another executor resumes or supersedes the work. Planning and reads remain concurrent, but execution revalidates state after acquiring the lease.
