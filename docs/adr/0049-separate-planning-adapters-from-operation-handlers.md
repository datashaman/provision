# Separate planning adapters from operation handlers

Provider integration uses two related seams: Implementation Adapters let the Planner discover capabilities, observe components, and produce typed operations; Operation Handlers let the Executor observe, apply or resume, verify, and recover those operations. The Planner retains cross-component ordering while adapters retain provider transition knowledge. This prevents provider details from leaking into the core without forcing planning and side effects through one oversized interface.
