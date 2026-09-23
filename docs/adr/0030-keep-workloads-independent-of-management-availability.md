# Keep workloads independent of management availability

Deployed applications continue operating when Provision is unavailable, and local or remote-host operation does not require a mandatory vendor-hosted control plane. Provision stays outside request, queue-processing, secret-reading, and schedule-execution paths unless an implementation explicitly declares otherwise. Optional hosted coordination may add shared history, approvals, or automation, but management failure pauses management work rather than application runtime.
