# Expose provider options behind a portable core

Provision configuration will express portable component intent while allowing explicit, namespaced implementation options for providers such as AWS or systemd. Hiding every provider detail would make important capabilities inaccessible, while provider-first configuration would destroy portability; namespaced options preserve both an honest common model and a deliberate escape hatch.

