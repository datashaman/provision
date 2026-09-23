# Use structured Action artifacts

Custom Actions reference either a digest-addressed container image or immutable executable artifact plus argument vector. Their inputs, outputs, timeouts, retry safety, checkpoints, network needs, credentials, and produced artifacts are declared; unstructured shell strings are not the contract. Execution adapters run compatible Actions locally, over SSH, in ECS or Fargate, or in Lambda. This keeps arbitrary application code bounded, reviewable, and portable across execution mechanisms.
