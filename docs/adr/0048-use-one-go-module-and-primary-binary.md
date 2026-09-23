# Use one Go module and primary binary

Provision begins as one repository, one Go module, and one primary `provision` binary, with the core modules and adapters kept in internal packages. A future coordinator may add a command in the same repository but must call the same behavioral core. This keeps one authoritative implementation and one distributable tool while avoiding premature service boundaries.
