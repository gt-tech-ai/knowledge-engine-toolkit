# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [Unreleased]

## [0.0.10] - 2026-10-17

Python: `techai-webutils` 0.5.3.

### Changed

- Breaking: `workflow.Base` is an embeddable base like every other tier's. A workflow embeds it
  and runs each operation as a read (`Exec`, no transaction), a command (`ExecTx`, one
  transaction) or a saga (`Exec` whose body commits each local step with `InTx`, undecorated).
  `NewBase(name, tx, deps)` takes the workflow's name. `Func`, `Wrap`, `Read` and `Tx` are
  removed. Python: `WorkflowBase(name, tx, deps)` with `exec`, `exec_tx` and `in_tx`; `func`,
  `wrap`, `read` and `tx` are removed.
- Breaking: a workflow's series and spans are labelled by the workflow's name and the operation
  (`{workflow=<name>, operation=<op>}`, span `<name>.<op>`), in place of
  `{workflow=<op>, operation=Execute}`.
- `ExecTx` and `InTx` (Python `exec_tx`, `in_tx`) fail INTERNAL on a context already in a
  transaction, or with no transaction manager, without running their body.
- Breaking: the outbox relay moves from `services/outbox` to `workflows/outbox`, since a pass is a
  saga. `NewRelay(cfg, tx, store, sink, …)` takes the transaction manager its writes commit
  through: the claim commits in one transaction, delivery runs with none open, and each row's
  finalize commits in its own. An outcome is counted once its finalize commits.
- Breaking: the `postgres` outbox store's writes (`Claim`, `MarkSent`, `Retry`, `Park`,
  `DeleteSentBefore`) run on the `*sql.Tx` bound to the context and fail INTERNAL when none is;
  `Stats` reads on the bound transaction when there is one. `outboxtest.Harness` takes the `Tx`
  its writes run in.

### Added

- `store.Base.Write(ctx, op, fn)`: run a store write through the store tier only inside a
  transaction; outside one it fails INTERNAL without running `fn`.
- Python `core.interfaces.repository.in_transaction()` and `marking_transaction()`, the mirror of
  Go's `interfaces.InTx` and `WithInTx`.
- `workflow.Base.Pass(op, pass)`: run a background loop's pass as one decorated operation with no
  transaction; its method value is a `loop.Decorator`, in place of `Wrap`. Go only, as
  `workflows/loop` has no Python mirror.
- `stores/sqltx`: the `database/sql` transaction manager. It binds the open `*sql.Tx` to the
  context (`sqltx.Tx(ctx)`), runs after-commit hooks, and fails INTERNAL on a nested
  transaction.
- `tests/fixtures.Must(v, err)`: a test's wiring of a `(T, error)` constructor, panicking on the
  error.

### Fixed

- `stream.NewMetrics`: the depth gauges could stay one item off for as long as the producer was
  blocked, when the producer's and the consumer's updates raced; each change and its report now
  happen under one lock.

## [0.0.9] - 2026-10-17

### Added

- `core/interfaces.OutboxRepository` and `repository.Base.Outbox(store)`: the outbox store run
  through the repository tier. `Claim` runs without retry (it leases rows and counts an
  attempt); `MarkSent`, `Retry`, `Park`, `Stats` and `DeleteSentBefore` get the full chain.

### Changed

- `outbox.NewRelay` takes an `interfaces.OutboxRepository`. The method set is the
  `OutboxStore`'s, so existing callers compile; build the argument with
  `repository.Base.Outbox` to put the relay's store calls behind the repository tier.

## [0.0.8] - 2026-10-17

Python: `techai-webutils` 0.5.2.

### Added

- `decorate.LoadDecorations(loader, collaborators)` (Python `Decorations.load`): read the
  decoration section and build the `Decorations` in the one call a composition root makes.
- A generated `MetricsQuerier` mock.
- `tools/decorgen` (`go run …/go/tools/decorgen/cmd/decorgen -source <pkg> -out <file>
  <Interface>…`): generates a `Decorated<Name>` per interface whose context-first,
  error-last methods run through a `decorate.Subject` (named by the method) and whose other
  methods pass through — the Go counterpart of Python's runtime `decorate(client, base)`.

### Changed

- Breaking: `job.Base.Local(name, group, fn)` replaces `job.Base.Runnable` and
  `core/interfaces.Runnable` (and its mock): a runner-less job is a name, a group and a
  function, with no adapter type to implement.
- Breaking: a Connect client call is retried only when its schema declares the procedure
  idempotent (`idempotency_level` `NO_SIDE_EFFECTS` or `IDEMPOTENT`); a procedure of unknown
  idempotency runs at most once, since its first attempt may have landed.

### Removed

- Breaking: the config fields the decoration tiers replaced — `transport.rate_limit`
  (`transport.RateLimitConfig`; a controller's rate limit is `decoration.controllers`),
  `replay.op_timeout` (`infra.ReplayConfig.OpTimeout`; the replay client's timeout is
  `decoration.clients`), `pipelines.timeout` (`pipelines.Config.Timeout`) and the
  `schema/workflows` package (`workflows.timeout`); a pipeline's and a workflow's timeout
  are `decoration.pipelines` and `decoration.workflows`.

### Fixed

- `decoration.Load` keeps a dotted component name (`outbox.sqs`, `seed.ingress.gateway`): the
  layered YAML loader split such an override key into nested maps, so it decoded as an empty
  override of its first segment and was dropped. An override may be written as one dotted key
  or nested by segment.

## [0.0.7] - 2026-10-13

### Added

- Python parity with the Go repository, service, pipeline and workflow tiers:
  `foundation/decorate` (`OpMiddleware`, `chain`, `exec_op` and the recovery, tracing, metrics,
  logging, auth, timeout, retry and circuit-breaker middlewares); `repos.repo_op_chain` +
  `RepoOpDeps` and `services.service_op_chain` + `ServiceOpDeps`, which decorate custom repository
  queries and domain service operations with the tier's CRUD stack; `DelegatingRepository` and
  `DelegatingCrudService`, so a consumer type inherits decorated CRUD and adds only its custom
  ops; `ServiceBuilder.with_validation`, `with_metrics` and `with_tracing`; `pipelines.then`
  (Go `pipeline.Stage.Then`); and `workflows.tx_workflow` (Go `workflow.Base.Tx`).
- One shape for the Go request tiers (pipeline, workflow, service, repository), on one engine:
  `decorate.Tier` (a tier's name and layer order, as data), `decorate.Deps` and
  `decorate.Subject` (`Exec[R]`), plus role decorators (`decorate.Reader`, `Lister`, `Writer`,
  `Inserter`, `Deleter`, `Exister`) from which every decorated CRUD shape is composed. Each tier
  package is one file with a `Tier()` and a `Base` whose helpers are generic methods (Go 1.27):
  `pipeline.Base` (`Stage`, `FanOut`; `pipeline.Stage.Then`), `workflow.Base` (`Func`, `Wrap`,
  `Read`, `Tx`), `service.Base` (embeds `Exec`; `CRUD`, `CRUDNoList`, `OpService`) and
  `repository.Base` (embeds `Exec`; `CRUD`, `ReadListExist`, `CRUDNoList`, `TxSafeInsert`), with
  `repository.Cached` for the read-through entity cache.
- The store tier, `go/stores`, between the clients and the repositories. `stores/store`:
  `Tier()` (Tracing → Metrics → Logging → Timeout, decorated by `decoration.stores`),
  `Decorate(store, name, deps)`, and `Base[C]` (`NewBase(name, client, deps)`; `Exec` through
  the tier, `Client(ctx)` the transaction-bound client) with `WithClient` / `ClientFrom`; kinds
  `KindEnt` / `KindGORM`, selected by `stores.kind`. `repository.DecoratedFromConfig` puts its
  store behind the store tier.
- `core/interfaces.ReadListExistStore`, `CRUDNoListStore`, `CRUDNoListRepository` and
  `InsertOnlyStore`, with generated mocks for every role interface and `OpService`;
  `fixtures.Workflows(tx)` for tests that build workflows.
- Configurable decoration for every tier. `foundation/config/schema/decoration` (`Config`,
  `Load`, `DefaultConfig`, `Tier`, `Layers`, `Merge`) gives the clients, workflows, pipelines,
  services, repositories, stores and jobs tiers each `defaults` plus per-component `overrides`
  keyed by the component's name; every layer (recovery, tracing, metrics, logging, auth, timeout,
  retry, circuit breaker, bulkhead) is switched and tuned there, and a partial section changes
  only what it sets. `decorate.Decorations` (`NewDecorations(cfg, Collaborators)`, `Deps(tier,
  name)`) and `decorate.Build` turn a component's resolved layers into its `decorate.Deps`
  (`Deps.SkipRecovery` drops recovery). The default keeps today's behavior: clients, workflows
  and jobs observed, the pipeline, service, repository and store tiers not; retry and a breaker
  on repositories; a 30 s timeout, bulkhead and breaker on the `storage`, `outbox.sqs`,
  `outbox.s3` and `prometheus` clients.
- The execution substrate in the same shape: `job.Tier()` and `job.Base` (`Func`, `Runnable`,
  `Wrap`), which decorate every job once; `engine.Pool` (`FanOut` as a generic method);
  `engine.Step` / `engine.Steps` (`Serial`); and a chainable `*gate.Gate` (`Observe`, `Run`,
  `RunLocal`).
- The clients tier in the same shape. `clients/client` holds the client decoration tier
  (`client.Tier()`: Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging),
  `client.Base` (`Once`, `Do`, `DoOnce`, `Stream`), `client.NewHandlerBase` (observed, never
  retried — a message handler) and the kind registry `client.Kinds[C, D, T]` (`New`, `Build`,
  which takes the family's layers from `Decorations` under the client's `Config.Name`). Every
  family is a root `<family>.go` (`Kind` constants, a data-only `Config`, a `Deps`, a kinds table,
  `NewFromConfig(ctx, cfg, deps)`), a `decorated.go` with `Decorate(base, name, deps)` where the
  family is decorated, and one subpackage per backend. `foundation/decorate` gains the
  `LayerBulkhead` layer (`decorate.NewBulkhead`, `Deps.Bulkhead`); its timeout codes a deadline
  overrun `CodeTimeout`.
- `clients/cache/stub` and `transport/server/stub`, so every family with a meaningful stub boots
  with no infrastructure (`database` and `rpc` default to `postgres` and `grpc`).
- `core/interfaces.CQLSession` (`CQLQuery`, `CQLIter`, `CQLBatch`, `CQLBatchKind`): the CQL
  contract the analytics store runs on, with generated mocks.
- The controller tier: `transport.Tier()` (tracing as a server span → metrics → logging →
  recovery → rate limit → bulkhead → auth → timeout), switched and tuned per controller by
  the new `decoration.controllers` tier; `transport.Base` (`NewBase`, `Handler`) runs a REST or
  RPC handler through it. `transport.Interceptors` lays the tier out for an RPC framework, and
  `transport/connect/interceptors.ServerOptions` (with `Edge{Authn, Inner}`) and
  `transport/grpc/interceptors.ServerOptions` run every Connect and gRPC server RPC through it,
  their status-aware tracing, metrics and access logs standing in for the tier's own.
- `decorate.LayerRateLimit` (`NewRateLimit`, `Deps.RateLimiter`; `decoration.rate_limit`) and a
  shedding bulkhead (`Deps.ShedLoad`; `decoration.bulkhead.shed`, `bulkhead.Shed`): an operation
  over the rate, or arriving at a full bulkhead, fails `CodeResourceExhausted` at once.
  `decorate.Tier.Middleware` builds one layer, for protocol adapters.
- `clients/rpc/wire.MetadataCarrier`, the OTel carrier over gRPC metadata.
- Python (`techai-webutils` 0.5.1): every tier decorated like Go's, from the same `decoration`
  config section. `foundation/config/decoration` (`DecorationConfig`, `Tier`, `Layers` and the
  retry, circuit-breaker, bulkhead and rate-limit tunings; `default_config`, `from_mapping`,
  `load`) reads the YAML the Go services read, with Go durations parsed by
  `foundation/config/duration.parse_duration`. `foundation/decorate` (a package now) adds
  `Layer`, `Tier`, `Subject`, `Deps`, `Observability`, `Decorations`, `Collaborators` and the
  bulkhead (waiting or shedding) and rate-limit middlewares; `foundation/resilience/retrier`
  adds `ExponentialRetrier`. Each tier has Go's `tier()` and a base built
  `from_decorations(name, decorations)`: `clients.client.ClientBase` and `decorate` (a client
  whose every coroutine method runs through the client tier), `controllers.ControllerBase`,
  `repos.RepositoryBase`, `services.ServiceBase`, `pipelines.PipelineBase` (`stage`, `fan_out`;
  `Stage.then`), `workflows.WorkflowBase` (`func`, `wrap`, `read`, `tx`) and
  `execution.job.JobBase`. `core/interfaces/tracer.SpanKind` sets a span's kind.
- Python gRPC server and client on the controller and client tiers.
  `controllers/grpc/interceptors.server_interceptors(name, deps, Edge(authn, inner))` lays the
  controller tier out as `grpc.aio` interceptors (`controllers.interceptors.lay_out`, the
  `transport.Interceptors` twin) with gRPC-native `TracingServerInterceptor`,
  `MetricsServerInterceptor` (`grpc_server_requests_total`, `grpc_server_duration_seconds`) and
  `LoggingServerInterceptor`; `service_auth` and `ValidatingServerInterceptor` are its edge
  interceptors. `clients/rpc/grpc/interceptors.client_interceptors` runs each unary call through
  the client tier and injects the trace context and the service token;
  `new_rpc_channel_from_config(config, decorations)` installs it, named by the new
  `RpcConfig.name` and carrying `RpcConfig.service_token`.
- Python messaging on the client tier, as Go's: `clients.messaging.decorate` runs each publish
  through the client tier under the new `MessagingConfig.name` (op: the topic), and both
  backends run every handler call through the handler tier as `<name>.handler`. The publisher's
  trace context rides SQS message attributes and memory-broker metadata, and the handler
  continues it (`foundation/tracer.inject` / `continued`). `new_messaging_from_config` and
  `new_messaging_subscriber_from_config` take `decorations` and a shared memory `broker`.

### Changed

- Breaking: server transport moved out of `clients/` into `transport/`: `clients/transport` →
  `transport/server` (`NewFromConfig`, `Config`, `KindStub`, `KindConnect`) with
  `transport/server/stub`; the Connect server → `transport/connect`; the Connect server
  interceptors → `transport/connect/interceptors`; the gRPC server and its tracing, metrics and
  logging interceptors → `transport/grpc` and `transport/grpc/interceptors`. The Connect client
  options moved to `clients/rpc/connect/interceptors.ClientOptions`.
- Breaking: removed the hand-ordered server builders and the interceptors the controller tier
  replaces — Connect `NewServerBuilder`, `RecoveryInterceptor`, `RateLimitInterceptor`,
  `BulkheadInterceptor`; gRPC `NewServerBuilder`, `RecoveryServerInterceptor`,
  `RateLimitServerInterceptor`, `BulkheadServerInterceptor` — and `transport/decorators`
  (`HandlerBuilder`), replaced by `transport.Base`. A server's observability now wraps its
  recovery and load shedding, so a rejected or panicking RPC is still traced, counted and logged.
- Breaking: the JSON:API codec moved to `foundation/jsonapi` — the route table (`Role*`,
  `RouteEntry` with the new `Resource()`, `ResourceConfig`, `Config.MatchRoute`), the
  `Transform*` functions and `MemberCode`; `transport/jsonapi` keeps `Middleware` (now taking a
  `foundation/jsonapi.Config`) and `IsRESTRequest`. The generic converters moved from
  `transport/rpc` to `foundation/convert` (`EnumMapper`, `NewEnumMapper`, `ParseUUID`,
  `Int32OrPanic`, `CountToInt32`); `transport/rpc` keeps the Connect error mapping.
- Breaking: the request tiers' builders, decorator subpackages and pass-through bases are
  replaced by the tier `Base`s. Removed: `pipeline.BasePipeline` / `NewBasePipeline` /
  `pipeline.Then`; `workflow.BaseWorkflow` / `NewBaseWorkflow` / `NewTxWorkflow`; the
  `pipelines/pipeline/decorators`, `workflows/workflow/decorators`, `services/service/decorators`
  and `repos/repository/decorators` packages; `service.BaseService` / `service.New` /
  `NewCRUDNoListService` / `OpChain`; `repository.BaseRepository` / `repository.New` /
  `NewReadListExistRepository` / `NewCRUDNoListRepository` / `NewTxSafeInsertRepository` /
  `OpChain` / `NewOpChain` / `OpDeps`; the `services` package (`Base`, `Build`); `decorate.MetricsSpec`;
  `interfaces.DecoratedRepository` / `DecoratedService` (use `Repository` / `Service`).
  `decorate.NewLogging`, `NewTracing`, `NewMetrics` and `NewRecovery` take `(…, tier, name)`.
  `repository.DecoratedFromConfig` keeps its signature and returns `interfaces.Repository`.
- Breaking: every request tier emits `<tier>_operations_total`, `<tier>_errors_total` and
  `<tier>_operation_duration_seconds` labelled `{<tier>, operation}` (pipelines and workflows
  emitted `<tier>_executions_total` / `<tier>_execute_duration_seconds` by name only; the
  repository label was `repo`), and spans carry `<tier>.name` / `<tier>.operation`.
- Breaking: the execution substrate drops its builders and standalone runners. `job.Adapt` →
  `job.Base.Runnable`; `job/decorators` → `job.Base` (jobs emit `job_operations_total` /
  `job_errors_total` / `job_operation_duration_seconds` in place of `execution_job_runs_total` /
  `execution_job_duration_seconds`, and a job whose results fail is no longer a `failed` outcome);
  `engine.FanOut(ctx, items, n, fn)` → `engine.Pool(n).FanOut(ctx, items, fn)`;
  `engine.NewStep` / `StepWarnOnly` / `RunStepsSerial` → `engine.Step{…}` / `engine.Steps.Serial`;
  `gate.NewGate(…)…Build()` → `gate.New(…)…`, `WithObserver` → `Observe` (nil keeps the
  current observer), `gate.RunGate(ctx, r, root, g)` → `g.Run(ctx, r, root)` and
  `gate.RunGateNoRunner(ctx, g)` → `g.RunLocal(ctx)`. Removed as unused: `job.New` / `Builder` /
  `Job` / `JobConfig` / `Single` and its options, `job.When` / `Conditional`, `job.Tag` /
  `TaggedJob` / `FilterByLang` / `LangTag`, `engine.WorkUnit`, `engine.RunStepsParallel`,
  `engine.StepFunc` and `engine.WithObserverCtx` / `ObserverFromCtx`.
- Breaking: client kinds are strings (`client.Kind`) and every family builds through
  `NewFromConfig(ctx, cfg, deps)` with its runtime collaborators (observability, SDK clients,
  shared Redis client, …) in a `Deps`, never in `Config`. Removed: each family's `New(kind,
  opts...)`, functional options (`With*`), `Kind.String()`, `ParseKind` and `ToOptions`;
  `messaging.NewPublisher` / `NewSubscriber` / `NewAdapter` (use `messaging.NewFromConfig` /
  `NewConsumerFromConfig`); `storage.New`; `prometheus.New` / `HTTPDoer` / `DecorateDoer` /
  `Sample`, now in `clients/prometheus/http`. `messaging.Config` takes the env-bound
  `infra.SQSConfig` plus `Receive` (nack delay, backoff, long poll); `outbox`, `lock`,
  `replaybuffer`, `prometheus` and `analytics` take their client-tier tuning as `Client
  client.Config` (outbox's `Resilience` is renamed). `outbox/sqs.New` and `outbox/s3.New` take
  the SDK client as an argument instead of `Config.API`. `connector.NewSourceBuilder(cfg, deps)`.
  `sqs.NewPublisher` and `sqs.NewSubscriber` take a `ctx` (`NewPublisherContext` is removed).
- Breaking: `clients/cassandra` is renamed `clients/cql`, with string kinds `cassandra` (default)
  and `keyspaces`; each kind subpackage dials through the gocql adapter in `clients/cql/driver`
  (`cassandra.Cluster` / `keyspaces.Cluster` build the configuration without dialing).
  `cql.Session` and friends move to `core/interfaces.CQLSession` etc. `analytics.Config`'s
  `Cassandra` field is `CQL`, and `analytics.Deps.Dial` replaces `SessionFactory`.
- Breaking: one decoration engine for the clients tier. Removed: `clients/decorators` (`Stack`),
  `foundation/decorator`, and the `analytics`, `cache`, `connector`, `jobs`, `lock`,
  `messaging`, `outbox`, `replaybuffer` and `storage` `decorators` packages. Decorated clients
  emit `client_operations_total` / `client_errors_total` / `client_operation_duration_seconds`
  labelled `{client, operation}`, replacing `lock_operation*`, `replaybuffer_operation*`,
  `messaging_handle_*` (a subscriber's handler is the client `messaging.handler`) and the
  analytics store's `analytics_executions_total` / `analytics_execute_duration_seconds` /
  `analytics_errors_total`. `cache` is decorated fail-open (a tier failure reads as a miss) and
  keeps `cache_hits_total` / `cache_misses_total` labelled `{cache}`;
  `cache_operation_duration_seconds` is `client_operation_duration_seconds{client="cache"}`.
- Breaking: decoration comes from config, not hand-passed collaborators. Every client family's
  `Deps` embeds `decorate.Decorations` (and its `Config` gains `Name`, replacing `Client
  client.Config`); `client.Config` / `DefaultConfig` / `DepsFromConfig`, `decorate.Tiers` /
  `NewTiers` and `reswire.NewRetrier` / `NewBreaker` / `NewBulkhead` are removed.
  `repository.DecoratedFromConfig` takes `Deps{Decorations, Cache, IDOf}`, returns an error, and
  `Settings` keeps only `CacheTTL` / `CacheVersion`. `river.NewInsertClient` takes the
  `decorate.Decorations` and runs each insert as the `river.insert` client
  (`river.InsertClientName`), resolved from `decoration.clients`. The messaging SQS and Redis
  backends take a ready-built `Handlers client.Base` in place of `Metrics` / `Tracer` /
  `HandlerTimeout`.
- Breaking: RPC clients run on the client tier. `clients/interceptorcore` and the per-concern
  client interceptors are removed — gRPC `ClientBuilder`, `StreamingClientBuilder`,
  `Retry*`, `CircuitBreaker*`, `Timeout*`, `Metrics*Client*`, `Tracing*Client*`,
  `Logging*Client*` and `ServiceAuth*ClientInterceptor`; Connect `ClientBuilder`,
  `RetryInterceptor`, `CircuitBreakerInterceptor`, `TimeoutInterceptor` and
  `ClientTracingInterceptor`. In their place `interceptors.ClientOptions(base, serviceToken)`
  (gRPC) and `interceptors.ClientOptions(base)` (Connect) run each call through a
  `client.Base`, and `rpc.NewFromConfig` installs them (`rpc.Config` gains `Name` and
  `ServiceToken`; `rpc.Deps` embeds `decorate.Decorations`). RPC client series are
  `client_*{client, operation="pkg.Service/Method"}`, replacing `grpc_client_*` and
  `grpc_client_stream_*`; a retried call returns its last error rather than Unavailable
  "retries exhausted". A gRPC stream's open is never retried.
- `clients/rpc/wire` (`FromError`, `Code`, `ToGRPC`, `ToConnect`): the RPC-status ↔ domain-code
  codec; `transport/rpc.FromRPCError` delegates to it. `Unimplemented` now maps to
  `CodeInvalidInput` (was `CodeUnknown`), so it is never retried.
- `client.Base.Open`: open a long-lived stream with the client's timeout bounding only the open.
  The client tier's spans are `SpanKindClient` (`decorate.Tier.SpanKind`; `decorate.NewTracing`
  takes the kind). `bulkhead.Shed` runs a call only when a slot is free (the server bulkhead
  interceptors use it).
- Fixed: the gobreaker circuit breaker's rejection (open, or half-open over quota) is
  `CodeUnavailable` instead of an uncoded error. The exponential retrier's `MaxRetries` counts
  retries as documented — it made `MaxRetries` total attempts, one fewer than configured.
- Breaking: persistence moves to `go/stores`. `repos/repository/compiler/ent` and
  `repos/repository/entlist` → `stores/ent` (`compiler.Apply` → `ent.Apply`); the
  `repos/repository/compiler` Kind enum (with its never-implemented mongo / elastic kinds) is
  removed; `repos/adapters/gorm` → `stores/gorm` (`Kind` is the string `"postgres"`);
  `repos/errors` → `stores/errors` (`NewDBMapper(kind string)`, `KindPostgres = "postgres"`).
  Config: `repos.compiler.kind` → `stores.kind`; `repos.timeout` is removed (the decoration
  config's repositories tier owns it).
- Breaking: `core/errctx` is merged into `core/errors`: `errctx.WithSink` / `Capture` /
  `Captured` are `errors.WithSink` / `Capture` / `Captured`.
- Breaking: the outbox store moves to the store tier. `repos/outbox/pgstore` →
  `stores/outbox/postgres`; `stores/outbox.NewFromConfig(ctx, cfg, deps)` builds it by kind
  (`KindStub` default, `KindPostgres` over `Deps.DB`) behind the store tier, named `Config.Name`
  (default `outbox`). `interfaces.OutboxStore` gains `DeleteSentBefore`. The test-only
  `outboxtest.SQLStore` reference store is removed; the conformance suites run against the
  postgres kind.
- A message handler continues its publisher's trace: `tracer.Inject` / `tracer.Extract`
  carry the process propagator's context (traceparent, tracestate, baggage) on SQS message
  attributes and memory-broker metadata, and the SQS subscriber hands a message's string
  attributes to the handler as `Message.Metadata`. Redis Pub/Sub carries the payload alone, so
  a trace stops at a Redis channel.
- Removed the unused jobs client: `clients/jobs` (`NewFromConfig`, `Config`), `core/interfaces`
  `Job`, `Worker`, `PeriodicJob`, `JobEnqueuer`, `JobScheduler` and `WorkerRegistry`, and the
  River `Client` / `Config` / `EventPublisher` / periodic jobs / workers;
  `clients/jobs/river` keeps `InsertClient`, `Migrate` and `Runtime`. Also removed the dead
  `messaging/sqs.Adapter`.
- Breaking (Python, 0.5.1): the tier builders, op chains and proxies are replaced by the tier
  bases. Removed: `foundation.decorator`; `repos` `RepositoryBuilder`, `BaseRepository`,
  `repo_op_chain`, `RepoOpDeps`; `services` `ServiceBuilder`, `BaseCrudService`,
  `service_op_chain`, `ServiceOpDeps`, the op-service `build`; `pipelines` `PipelineBuilder`,
  `AsyncPipelineBuilder`, `BasePipeline`, `BaseAsyncPipeline`, `then`; `workflows`
  `WorkflowBuilder`, `AsyncWorkflowBuilder`, `BaseWorkflow`, `BaseAsyncWorkflow`, `tx_workflow`;
  `controllers.decorators.HandlerBuilder`; `execution.job.decorators`; the client proxies
  (`LoggingProxy`, `RetryProxy`, `CircuitBreakerProxy`, `MetricsProxy`, `HedgeProxy`,
  `RateLimitProxy`, …) with `new_client_stack_from_config` / `ClientStackConfig`, and the
  `event_stack` / `job_stack` helpers. The synchronous pipeline and workflow variants are gone.
  The tier series follow Go's names: `<tier>_operations_total`, `<tier>_errors_total` and
  `<tier>_operation_duration_seconds`, labelled `{<tier>, operation}` (the client tier drops the
  former `outcome` / `code` labels).
- Breaking (Python): the gRPC server moved under the controller tier. `clients.transport.grpc`
  (`GracefulServer`, `ServerConfig`, `create_server`), `clients.rpc.grpc.errors` and
  `clients.rpc.grpc.interceptors.auth` → `controllers.grpc`; `ServerInterceptorBuilder` and the
  client `InterceptorBuilder` with its retry, timeout, circuit-breaker, logging, metrics and
  tracing interceptors are removed — use `server_interceptors` and `client_interceptors`.
- `techai-webutils` versions: 0.5.0 carried the Python tier additions above (no Go change); 0.5.1
  carries the breaking Python changes above.
- Breaking (Go): `transport/grpc/interceptors.ServerOptions(name, deps, edge)` takes an `Edge`
  (`Authn` unary and `StreamAuthn` stream interceptors, run just before Auth) and now decorates
  stream RPCs too (no timeout on a stream); `Tracing`, `Metrics` and `Logging` gain
  `*StreamServerInterceptor` twins.
- The controller tier is the edge (`decorate.Tier.Edge`, Go and Python): a REST handler's failure
  is logged at Warn for a client fault and at Error otherwise, where inner tiers log at Debug.
- Messaging spans carry their role, Go and Python: a publish is a PRODUCER span
  (`client.NewProducerBase` / `ClientBase.producer`) and a handler call a CONSUMER span
  (`client.NewHandlerBase` / `ClientBase.handler`), where both were CLIENT spans.
- `client.Kinds.AtMostOnce` lists the kinds `Build` never retries; the Redis messaging
  publisher uses it in place of a special case in `messaging.NewFromConfig`.
- Python: a decorated client (`clients.client.decorate`) forwards `async with` to the client
  it wraps; a refused auth layer raises `ForbiddenError("<name>.<op>: not allowed")`.
- A controller's bulkhead sheds by default once enabled (`decoration.controllers` defaults
  `bulkhead.shed: true`; the bulkhead itself stays off), Go and Python.
- Recovery logs a panic with its stack even when the tier's logging layer is off
  (`decorate.Deps.PanicLogger` / Python `Deps.panic_logger`, set by `Decorations` whenever
  recovery is on).

### Fixed

- Python: a `null` decoration section (an empty YAML key) decodes as its default instead of
  `None`.
- A tier rejection on the Connect and gRPC servers (a denial, a rate limit, a panic) answers
  only its code's client-safe message (`clients/rpc/wire.SanitizeConnect` / `SanitizeGRPC`;
  Python `controllers.grpc.safe_status`); the real error is captured for the access log instead
  of reaching the caller.
- A gRPC server continues its caller's trace: `TracingServerInterceptor` extracts the W3C
  trace context the client injected into the incoming metadata instead of starting a new
  trace per hop.
- Python: the gRPC client's span was tagged `kind="client"` as an attribute instead of being a
  CLIENT span, and the client injected neither the trace context nor the service token; both
  now ride every call. A rate-limited Python gRPC call was answered UNIMPLEMENTED; the
  controller tier answers RESOURCE_EXHAUSTED.

## [0.0.6] - 2026-10-12

### Added

- `workflows/loop.Spec` + `NewGroupFromSpecs`: a service declares its background loops as
  data (run, name, op, schedule) and the composition root builds one decorated `Group`.
- `core/interfaces.KeyedLimiter` and its in-process backend `foundation/resilience/bulkhead/keyed`:
  a non-blocking per-key concurrency cap (one tenant cannot take every slot).
- `foundation/config.Section[T]`: unmarshal and validate one typed config section (value- or
  pointer-receiver `Validate`); `foundation/tracer.NewFromTracing`: a service's tracer from
  the tracing section, falling back to noop so a missing collector never blocks boot.
- `foundation/chartspec.Validate`: validate an already-decoded `types.ChartSpec` exactly as
  `Parse` validates JSON, so a proto-mapped spec needs no JSON round trip. A shelf node with
  both a field and an operator is now `CodeInvalidInput` on both paths.
- `repos/outbox/pgstore`: the production PostgreSQL `OutboxStore` over a product's own outbox
  table, named by config (table, key, tenant and attribute columns, lease, error cap), plus
  `DeleteSentBefore` for retention; proven against the `outboxtest` conformance suite.
- `repos/repository.OpDeps` + `NewOpChain`: a repository's custom-op chain built from one
  bundled value instead of six positional collaborators.
- `foundation/resilience/pausegate`: a receive gate that closes for a configured pause after an
  outage-coded error (default `CodeUnavailable`), with an optional paused gauge, so a consumer
  stops pulling messages it cannot process during a downstream outage.

### Changed

- Python `new_client_stack_from_config` is generic: it returns the stack typed as the client it
  was given, so callers no longer `cast` the result.
- `techai-webutils` is published as 0.4.0. PyPI already holds 0.1.0 through 0.3.3 from the
  package's earlier release line, which also made those the newest by version; continuing above
  them means no collision and an unpinned install gets this line. The release PR now sets the
  Python version by hand (AutoGitSemVer is removed: the repo's Go `v*` tags reset its count).
- Releases are cut by merging to `main` (`scripts/release.sh`): the Go module is tagged `vX.Y.Z`
  from the newest `CHANGELOG.md` section, and the Python package is published and tagged
  `techai-webutils-vX.Y.Z` from its pyproject version. A tag push no longer triggers a release,
  and CI's `release-check` fails a PR whose versions would re-release or go backwards.
- `core/interfaces.AnalyticsStore` embeds `HealthChecker` (breaking for out-of-tree
  implementations): the Cassandra store's `Readiness` runs `SELECT release_version FROM
  system.local` and is `CodeUnavailable` when stopped or unreachable; the stub is always ready;
  the decorated store delegates the probes undecorated.

## [0.0.5] - 2026-10-11

### Fixed

- `clients/cassandra` (self-managed kind): a cluster with `DisableHostDiscovery` selects hosts
  DC-aware round robin instead of token-aware. With one pinned endpoint token awareness routes
  nothing, and gocql v1.7.0's token-aware policy dereferenced a nil host once that endpoint
  left its ring during an outage, crashing the process from a driver goroutine. Discovering
  clusters keep the token-aware policy.

## [0.0.4] - 2026-10-11

### Added

- `clients/messaging/sqs.NewAWSClient`: builds the SDK SQS client the publisher and subscriber
  use (region, endpoint override, static or default-chain credentials) for a caller that
  sends through the SDK directly. An access key without its secret is `CodeInvalidInput`.
  No I/O until the first call.
- `go/tests/mocks`: generated mocks for `CacheInvalidator`, `Encryptor`, `EventPublisher`,
  `JobEnqueuer`, `JobScheduler`, `WorkerRegistry`, `Locker`, `CountingPublisher`,
  `MessageConsumer`, `Repository`, `Source` and `Transaction`, so a consumer imports the
  engine's mocks instead of regenerating them.
- `testdata/analytics_fact.golden.json`: a dim holding `<`, `>`, `&` and U+2028 pins that Go
  and Python escape them identically.
- `pipelines/pipeline.Then`: composes two pipelines into one, the first's output feeding the
  second. An error from the first is returned unchanged and the second does not run; each
  stage keeps its own decorators. A nil stage panics.
- `workflows/loop`: background work as decorated workflow passes. `Pass` adapts a
  `func(context.Context) error` into a `Workflow[struct{}, struct{}]`. `NewTicker` runs a pass
  per `Config.Interval` (back to back when zero; `RunAtStart` adds a startup pass), logs a
  failed pass and waits `ErrorBackoff`, and returns nil when its context ends. A negative
  duration is `CodeInvalidInput`. `Group` is an `interfaces.Lifecycle` that runs `Runner`s
  (`RunnerFunc` adapts a function) together for the lifetime of its `Start` context and logs
  one that stops with an error without stopping the others; a runner or pass that fails
  after the shutdown began is logged at debug, or not at all when it returns the context's
  own error; `Stop` cancels and waits within its context (`CodeTimeout` /
  `CodeCanceled`), and a second `Start` is `CodeConflict`.

## [0.0.3] - 2026-10-11

### Added

- `core/interfaces.Stream[T]`: a pull-based, closable sequence that every large result can
  travel on. `Close` may come from another goroutine and ends a running `Next`.
- `foundation/stream`: `FromSlice`, `Chunk` (greedy split at a byte budget; an oversized item
  is `CodeResourceExhausted`) and `Collect` (drain under a byte budget, closing the source
  on every path). `Buffer` decouples producer from consumer, selected by `Kind` + `Config` +
  `NewFromConfig` (`ParseKind`: `direct`, the default, or `bounded`, which reads ahead on a
  goroutine bounded by item count and bytes and blocks the producer when full). A context
  deadline ending a read is `CodeTimeout`; a cancellation is `CodeCanceled`. `NewMetrics`
  decorates a `Buffer` with the `stream_buffer_items` and `stream_buffer_bytes` gauges and
  the `stream_buffer_producer_wait_seconds` histogram, labelled `buffer`.

## [0.0.2] - 2026-10-11

### Changed (breaking)

- `foundation/listquery`: `Set`, `NewSet` and `ParseList` are removed. `ParseSort(m, spec)`
  sorts against the `Map`: only fields declared with `Field.Sortable()` may be sorted, and an
  unknown, unsortable or repeated field, an empty token or a bad direction is
  `CodeInvalidInput` instead of being dropped.
- `foundation/chartspec`: `CubeSchema` is replaced by `listquery.Schema`; `Compile` and `Drill`
  take it.
- `core/types.ChartSpec`: `FilterJSON json.RawMessage` is replaced by `FilterTree *FilterNode`
  (the wire key stays `filter`).

### Added

- `core/types.FilterNode`: the filter tree with a JSON codec for the listquery grammar, and
  `MaxFilterDepth`. `FilterOperator.Known` reports whether an operator is one of the defined
  set.
- `foundation/listquery.ParseFilter`: checks a `FilterNode` against a `Map`; `Parse` is now
  built on it. Integers outside ±(2^53−1) are rejected, and so is an empty or unknown
  operator in a tree built in code (`CodeInvalidInput`).
- `testdata/listquery/filters.golden.json`: the shared filter corpus.
- `foundation/chartspec`: a content-addressed node encoding for `ChartSpec`. `Node` and `Hash`
  (SHA-256 over `"chartspec/v1\x00"` and the node's RFC 8785 canonical JSON); `Encode` splits a
  spec into a `Tree` of shared nodes, `Decode` rebuilds it through a `NodeGetter` and verifies
  each node against its hash, and `Diff` reports the changed paths between two roots as
  `Change`s (a zero side is one root `Change`). `Encode` rejects an unknown filter operator,
  stores a nil `[]any` operand as an empty array, and treats a typed-nil field as absent. New
  dependency: `github.com/gowebpki/jcs`.
- `testdata/chartspec/nodes.golden.json` and `testdata/chartspec/specs.golden.json`: pinned
  node and root hashes.
- `clients/cassandra`: `CassandraConfig.DisableHostDiscovery` pins a cluster reached through
  a proxy, tunnel or port mapping to its one IP-literal contact point, translating every
  address the cluster advertises back to it; `ReconnectInterval` sets how often downed
  hosts are retried. `WithDisableHostDiscovery` and `WithReconnectInterval` set them.

## [0.0.1] - 2026-10-10

### Added

- Initial release of the Go module `github.com/gt-tech-ai/knowledge-engine-toolkit` and the
  Python package `techai-webutils`: the layered substrate (core → foundation → clients →
  repos → services → pipelines → workflows → transport), its `Kind` + `Config` factories,
  decorator stacks, the execution fan-out engine, and the test fixtures and generated mocks.
  `ARCHITECTURE.md` describes each layer.

[Unreleased]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.9...HEAD
[0.0.9]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.8...v0.0.9
[0.0.8]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.7...v0.0.8
[0.0.7]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.6...v0.0.7
[0.0.6]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.5...v0.0.6
[0.0.5]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/releases/tag/v0.0.1
