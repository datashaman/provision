# Use the same engine for automation

The Go binary provides stable CLI commands, structured JSON input and output, a machine-readable event stream, deterministic exit codes, and non-interactive approvals. CI and remote runners invoke the same engine used interactively. A runner is a one-shot process that receives a Plan digest and State Backend location, resolves its own credentials, acquires the fenced lease, executes or resumes, records the result, and exits; a future coordinator dispatches jobs rather than implementing another Executor.
