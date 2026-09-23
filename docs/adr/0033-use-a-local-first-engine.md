# Use a local-first engine

Provision's behavioral core is a local engine invoked by a CLI, so current-host, remote-host, and AWS workflows require neither a resident daemon nor a hosted account. A future coordinator and remote runner may add collaboration and delegated execution, but they use the same engine interfaces rather than reimplementing product behavior. This preserves local independence while leaving a clean path to shared operation.
