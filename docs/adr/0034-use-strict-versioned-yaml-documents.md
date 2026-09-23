# Use strict versioned YAML documents

Human-authored configuration uses versioned YAML documents under a restricted YAML 1.2 profile and strict schema, with equivalent JSON accepted for generated input. Unknown fields are errors, references and composition are explicit, and compilation produces one canonical model with source provenance. This provides approachable authoring without allowing YAML coercion surprises or configuration-time code execution.
