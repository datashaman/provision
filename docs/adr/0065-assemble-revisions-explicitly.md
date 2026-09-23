# Assemble revisions explicitly

Build Actions produce named immutable artifacts and evidence. A deterministic revision-assembly operation combines declared outputs from any repositories or pipelines into one complete application revision manifest. Deployment uses only that manifest, preserving reproducibility and promotion identity without depending on the invoking checkout or silently rebuilding code.
