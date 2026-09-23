# Exploratory configuration sketch

> Status: non-normative product-discovery material. This file does not define an accepted configuration format or technical architecture. Its `cache` examples and disposable-data assumption predate the agreed `key-value store` role and required Store Data Role; do not use them as current configuration guidance.

# Deployment configuration model

## Design boundary

The application topology declares logical component roles. An environment is a durable application instance with behavior, policy, secrets, domains, state, and deployment history. A deployment profile independently binds every component to a target and renderer. There is no global runtime choice: one profile may combine a systemd API, an ECS realtime service, SQS-backed workers, EventBridge Scheduler, RDS, and ElastiCache.

Targets describe where execution happens. Renderers describe how a component type is compiled into native artifacts for a target.

The three primary configuration concepts are:

- `Application`: portable topology and behavioral configuration contract.
- `DeploymentProfile`: reusable targets, renderers, triggers, and capability defaults.
- `Environment`: concrete deployment selecting one application and profile, with isolated configuration and a stable ledger identity.

These are files and deployment records, not resources in a continuously reconciled API server. The tool is a run-to-completion compiler and deployment runner:

```text
read configuration
→ validate capabilities
→ render native artifacts
→ show plan
→ apply once
→ verify or roll back
→ write deployment ledger
→ exit
```

Native systems remain responsible after the command exits. Systemd supervises processes, ECS and Lambda schedule compute, Application Auto Scaling changes capacity, EventBridge emits schedules, and managed data services maintain state.

Explicit non-goals:

- No permanent control plane or custom scheduler.
- No generic resource API, custom resource definitions, or user-written operators.
- No service mesh, overlay network, or replacement secrets system.
- No background reconciliation required to keep deployed applications alive.
- No renderer-defined component types; the vocabulary in this specification is closed.

## Environment configuration

Environment files are separate from the application and profile so the same immutable artifacts can be promoted without rebuilding them.

```yaml
environment: storefront-production
application: storefront
profile: aws-hybrid
lifecycle: persistent       # persistent | ephemeral

labels:
  stage: production
  owner: commerce

behavior:
  mode: production
  values:
    LOG_LEVEL: info
    PUBLIC_URL: https://shop.example.com
    EMAIL_DELIVERY: live
  featureFlags:
    recommendations: true
    experimentalCheckout: false
  components:
    realtime:
      presenceTimeout: 45s
    email-worker:
      batchSize: 20

domains:
  api: api.example.com
  realtime: realtime.example.com

secrets:
  provider: aws.secrets-manager
  refs:
    paymentApiKey: storefront/production/payment-api-key
    sessionSigningKey: storefront/production/session-signing-key

policy:
  deploymentStrategy: blue-green
  requireReversibleCutover: true
  approval: required
  allowDestroy: false
  databaseMigration: expand-migrate-contract

ledger:
  backend: aws.s3
  bucket: company-provision-state
  key: storefront/production
  locking: aws.dynamodb

overrides:
  components:
    api:
      scaling:
        min: 3
        max: 30
    realtime:
      scaling:
        min: 3
        max: 40
    database:
      availability: multi-zone
```

An ephemeral preview environment uses the same configuration shape with a bounded lifecycle:

```yaml
environment: storefront-pr-1842
application: storefront
profile: devbox
lifecycle: ephemeral
expiresAfter: 72h
labels:
  stage: preview
behavior:
  mode: preview
  values:
    LOG_LEVEL: debug
    EMAIL_DELIVERY: capture
  featureFlags:
    experimentalCheckout: true
policy:
  deploymentStrategy: blue-green
  approval: none
  allowDestroy: true
```

The deployment runner injects standard metadata into every executable component:

```text
APP_ENVIRONMENT=storefront-production
APP_STAGE=production
DEPLOYMENT_ID=<stable environment deployment id>
DEPLOYMENT_REVISION=<immutable revision>
DEPLOYMENT_COLOR=blue|green
```

Application behavior should depend on declared values and feature flags. `APP_STAGE` is useful for telemetry and diagnostics, but application code should not detect EC2, ECS, Lambda, or renderer names to choose business behavior.

Resolution order is deterministic:

1. Platform defaults.
2. Application defaults and configuration contract.
3. Deployment-profile defaults and capabilities.
4. Environment behavior, policy, and explicit overrides.
5. Generated bindings and secret references.

Secrets are resolved only during execution and are redacted from plans, logs, and stored deployment outputs.

## Logical application topology

```yaml
application: storefront

deploymentDefaults:
  strategy: blue-green
  requireCapability: true
  verification:
    healthChecks: true
    smokeTests: true
    stabilization: 10m
  rollback:
    automatic: true
    retainPrevious: 30m

channels:
  jobs:
    type: queue
    delivery: at-least-once
    ordering: best-effort
    deadLetter: true

components:
  database:
    type: database
    engine: postgres
    version: "16"
    lifecycle: shared
    durability: required

  cache:
    type: cache
    engine: redis
    version: "7"
    lifecycle: shared
    durability: disposable

  api:
    type: http-service
    uses: [database, cache]
    health:
      path: /health
    scaling:
      metric: requests
      min: 1
      max: 10

  realtime:
    type: realtime
    protocol: websocket
    uses: [database, cache]
    health:
      path: /health
    scaling:
      metric: active-connections
      min: 1
      max: 10
    rollout:
      drain:
        maxDuration: 15m
        reconnectCode: 1012

  email-worker:
    type: worker
    mode: consumer
    consumes: jobs
    uses: [database, cache]
    delivery: at-least-once
    idempotencyKey: message.id
    scaling:
      metric: backlog-per-worker
      target: 100
      min: 1
      max: 20

  cleanup-worker:
    type: worker
    mode: run-to-completion
    uses: [database]
    timeout: 30m

  scheduler:
    type: scheduler
    jobs:
      cleanup:
        schedule:
          cron: "0 2 * * *"
          timezone: Africa/Johannesburg
        target: cleanup-worker
        overlap: forbid
        retry:
          attempts: 3
```

Application artifacts are named independently from their renderer:

```yaml
artifacts:
  api-process:
    type: archive
    path: ./dist/api.tar.gz
  api-container:
    type: container
    image: example/storefront-api:1.4.0
  realtime-container:
    type: container
    image: example/storefront-realtime:1.4.0
  worker-process:
    type: archive
    path: ./dist/workers.tar.gz
  worker-container:
    type: container
    image: example/storefront-workers:1.4.0
  lambda-functions:
    type: lambda-zip
    path: ./dist/functions.zip
```

## Devbox profile

The devbox can be the current machine or an existing machine reached over SSH. Changing the connection does not change the component renderers.

```yaml
profiles:
  devbox:
    targets:
      host:
        driver: existing.host
        connection:
          driver: ssh       # use `local` for the current machine
          address: devbox.lan
          user: deploy
          hostKey: SHA256:replace-with-pinned-host-key
        privilegeEscalation: sudo

    implementations:
      database:
        renderer: local.postgres
        target: host
        supervisor: systemd
        dataDir: /var/lib/storefront/postgres

      cache:
        renderer: local.redis
        target: host
        supervisor: systemd
        dataDir: /var/lib/storefront/redis

      jobs:
        renderer: local.redis-queue
        target: host

      api:
        renderer: systemd.service
        target: host
        artifact: api-process
        command: ./storefront api
        traffic:
          renderer: local.nginx

      realtime:
        renderer: systemd.service
        target: host
        artifact: api-process
        command: ./storefront realtime
        traffic:
          renderer: local.nginx
          websocket: true

      email-worker:
        renderer: systemd.service
        target: host
        artifact: worker-process
        command: ./storefront worker email
        trigger: jobs

      cleanup-worker:
        renderer: systemd.oneshot
        target: host
        artifact: worker-process
        command: ./storefront cleanup

      scheduler:
        renderer: systemd.timer
        target: host
```

`local.cron` and `app.scheduler` are valid scheduler renderer alternatives for environments where systemd timers are unavailable or an application-owned scheduler is preferred.

## Mixed AWS profile

This example deliberately mixes implementations: the API remains on an EC2 host under systemd, realtime and workers use ECS/Fargate, schedules use EventBridge Scheduler, and state uses managed AWS services.

```yaml
profiles:
  aws-hybrid:
    providers:
      aws:
        region: af-south-1

    targets:
      app-host:
        driver: aws.ec2
        provider: aws
        instanceType: t3.medium
        image: ami-xxxxxxxx
        instanceCount: 1

      app-cluster:
        driver: aws.ecs-fargate
        provider: aws
        cluster: storefront

    implementations:
      database:
        renderer: aws.rds-postgres
        provider: aws
        instanceClass: db.t4g.small
        multiAz: true
        connection: aws.rds-proxy

      cache:
        renderer: aws.elasticache-redis
        provider: aws
        nodeType: cache.t4g.small

      jobs:
        renderer: aws.sqs
        provider: aws
        fifo: false
        deadLetter:
          maxReceives: 5

      api:
        renderer: systemd.service
        target: app-host
        artifact: api-process
        command: ./storefront api
        traffic:
          renderer: aws.alb

      realtime:
        renderer: ecs.service
        target: app-cluster
        artifact: realtime-container
        traffic:
          renderer: aws.alb
          websocket: true
        rollout:
          strategy: blue-green-drain

      email-worker:
        renderer: ecs.service
        target: app-cluster
        artifact: worker-container
        command: ["./storefront", "worker", "email"]
        trigger: jobs
        scaling:
          renderer: aws.application-autoscaling
          metric: sqs-backlog-per-task

      cleanup-worker:
        renderer: ecs.run-task
        target: app-cluster
        artifact: worker-container
        command: ["./storefront", "cleanup"]

      scheduler:
        renderer: aws.eventbridge-scheduler
        provider: aws
        target: cleanup-worker
```

For per-message isolated ECS jobs, a worker may instead use `aws.eventbridge-pipe` with an SQS source and `ecs.run-task` target. For continuously polling workers, `ecs.service` is preferred and scales from SQS backlog per running task.

## Renderer catalogue

| Component type | Example renderers |
|---|---|
| Database | `local.postgres`, `aws.rds-postgres`, `external.postgres` |
| Cache | `local.redis`, `aws.elasticache-redis`, `external.redis` |
| Realtime | `systemd.service` + proxy, `ecs.service` + ALB, `aws.api-gateway-websocket` + Lambda |
| Worker consumer | `systemd.service`, `ecs.service`, `lambda.event-source` |
| Worker run-to-completion | `systemd.oneshot`, `ecs.run-task`, `lambda.function` |
| Scheduler | `local.cron`, `systemd.timer`, `app.scheduler`, `aws.eventbridge-scheduler` |
| Queue channel | `local.redis-queue`, `rabbitmq.queue`, `aws.sqs`, `external.queue` |

Renderers advertise capabilities and emit native provider artifacts. The compiler validates protocol, trigger type, scaling metric, persistence, rollout mode, network reachability, and secret injection before producing a plan. Renderers cannot introduce new component types.

## Deployment-time handoff rules

### Database

Databases are shared between application colors by default. Application schema changes must follow expand/migrate/contract compatibility. A renderer may additionally advertise replicated blue-green database cutover, but the platform must not claim reversible data rollback unless the provider and migration plan genuinely support it.

### Cache (superseded sketch)

This historical sketch assumed every cache could be recreated or warmed. The agreed model instead uses a key-value store with an explicit Store Data Role. Authoritative contents must be preserved, while derived contents may be rebuilt and ephemeral contents may be discarded under their declared policies.

### Realtime

New WebSocket connections switch to green. Existing connections remain on blue during a bounded drain window. Blue is retired after all sessions close or the deadline is reached; remaining clients receive a reconnect signal. Seamless session movement requires session state outside the process.

### Worker

Green starts without consuming work, passes health checks, and becomes ready. The deployment runner asks the native runtime to stop blue from taking new jobs, waits for in-flight work, and then enables green consumers. Rollback reverses that handoff. At-least-once channels require idempotent handlers, visibility timeouts, retry limits, and dead-letter handling.

### Scheduler

Green schedules are rendered disabled. During apply, the deployment runner disables the native blue schedule, switches the native target, and enables green. The scheduled job uses a generation or deduplication key when the underlying scheduler cannot guarantee an atomic target update. No platform scheduler remains running after deployment.

## Scaling semantics

| Type | Portable scaling intent passed to the native renderer |
|---|---|
| Database | storage, connections, read replicas, availability class |
| Cache | memory, shards/nodes, eviction policy |
| Realtime | active connections, connection rate, messages per second |
| Worker | backlog per worker, oldest-message age, concurrency |
| Scheduler | normally single active; scale scheduled targets instead |

## Profile presets supported by the model

- Existing local machine or remote LAN/VPN devbox with systemd and local data.
- EC2 with systemd and host-local PostgreSQL/Redis.
- EC2 with systemd plus RDS/ElastiCache.
- ECS/Fargate services plus AWS-managed state.
- Lambda versions and aliases for compatible HTTP or event-driven components.
- Hybrid profiles where every component independently selects one of these implementations.

## Environment operations

```text
provision environment validate storefront-production
provision environment plan storefront-production
provision environment apply storefront-production
provision environment promote storefront-staging storefront-production --revision <revision>
provision environment destroy storefront-pr-1842
```

Promotion deploys an existing immutable revision into another environment. Each destination recompiles its own profile, behavior, secrets, scaling, domains, and policy into a native deployment plan. Promotion does not create an application-level control loop.
