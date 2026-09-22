# Model schedules rather than schedulers

A schedule is the first-class application component: it names a target task and declares timing, overlap, retry, and failure policy. A scheduler is an environment-specific implementation of that intent, such as cron, a systemd timer, an application cron runner, or a cloud scheduler. This keeps application meaning stable without treating infrastructure machinery as a logical component.
