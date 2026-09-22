# Separate deployment from promotion

Any revision may be deployed independently to any environment. Promotion is an optional workflow that deploys the exact same revision previously deployed or verified elsewhere; environment names never imply a pipeline. Source verification evidence is retained as provenance, but destination policy remains authoritative and may require fresh checks or approval. This supports both shared testing environments and pipeline-style progression without conflating them.
