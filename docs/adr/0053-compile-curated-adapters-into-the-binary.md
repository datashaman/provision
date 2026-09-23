# Compile curated adapters into the binary

Initial implementations are compiled into the `provision` binary and registered internally. Provision does not initially load Go dynamic plugins or third-party provider binaries. A future extension mechanism must use a versioned out-of-process protocol with capability negotiation, avoiding shared process memory and Go type coupling while leaving room for independently released implementations.
