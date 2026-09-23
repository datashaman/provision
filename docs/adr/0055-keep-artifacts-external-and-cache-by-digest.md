# Keep artifacts external and cache by digest

Provision references application artifacts in external OCI registries, HTTPS repositories, or object stores and verifies them by digest rather than becoming their registry. A local content-addressed cache, missing-only SSH transfer, and target-specific staging into ECR or S3 make execution efficient. This preserves compatibility with existing build systems while keeping artifact identity and policy verification inside the deployment contract.
