# Use a canonical JSON operation envelope

Plans represent every operation as canonical JSON with a small versioned envelope and a typed, kind-specific payload. The Executor owns common ordering, safety, timeout, and recovery fields, while the registered Operation Handler owns payload validation and meaning. This keeps Plans reviewable and digestable without forcing provider transitions into one universal schema or embedding executable code.
