# Glossary

## Application

The logical system and its component relationships, independent of a specific deployment destination.

## Environment

A named, concrete use of an application with its own behavior, policy, implementation choices, secrets, domains, sizing, and lifecycle.

## Component

A logical application role such as HTTP service, database, cache, realtime server, worker, scheduler, or queue.

## Implementation

One possible way to realize a component in an environment. Examples include systemd, ECS, Lambda, RDS, ElastiCache, cron, and EventBridge Scheduler.

## Execution target

The place where executable work runs, such as the current machine, a remote host, EC2, ECS, or Lambda.

## Remote devbox

An existing development machine reachable over a network. It may be on a LAN or VPN and is not necessarily the machine initiating deployment.

## Revision

An immutable built version of application code or configuration intended for promotion between environments.

## Blue-green deployment

A deployment in which an existing revision remains available while a candidate revision is prepared and verified, followed by a reversible handoff where possible.

## Worker

An asynchronous component that either continuously consumes work or runs to completion for an individual job.

## Scheduler

A component responsible for initiating work according to recurring or one-time schedules.

## Realtime server

A long-lived connection service, initially focused on WebSockets.

## Deployment profile

A possible reusable grouping of implementation choices. Whether profiles remain a first-class user-facing concept is still an open product question.
