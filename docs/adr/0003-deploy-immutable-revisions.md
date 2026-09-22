# Deploy immutable revisions regardless of where they are built

Provision will support both running application-defined builds and accepting artifacts built elsewhere, but every deployment consumes an immutable revision manifest. This boundary keeps promotion reproducible and prevents environment deployment from quietly rebuilding different code while allowing teams to retain existing CI pipelines.

