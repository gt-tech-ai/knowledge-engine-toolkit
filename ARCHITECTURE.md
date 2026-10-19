# Architecture

The rules every package here follows, cited from code comments as `ARCHITECTURE.md#<section>`.
Paths starting `go/` or `python/` are relative to the repo root. Otherwise a Go path is relative to
`go/`, a Python source path to `python/techai_webutils/src/techai_webutils/`, and a Python test path
(`tests/…`) to `python/techai_webutils/`.

## The one idea

Everything is an implementation detail behind an interface. The contract lives in `core`, which
imports no other layer, and that includes the policy seams a consumer implements (Python's
`PassagePolicy` is in `core/interfaces/retrieval.py`). The implementation is **selected by
configuration** (a `Kind`), **injected explicitly** through constructors, and **wrapped by
decorators** for cross-cutting concerns. Swapping S3 for memory, zap for slog, or asyncio for Ray
is a config change.

## Layers and import direction

A Go package imports only its own layer or lower ones: `go/core` (interfaces, types, errors,
events) → `go/foundation` (config, logging, telemetry, resilience,
decorator mechanisms, lifecycle) → `go/clients` (external systems) → `go/stores` (persistence
over an ORM) → `go/repos` → `go/services` →
`go/pipelines` → `go/workflows` → `go/transport` (REST/RPC edges).

Outside the ladder: `go/execution` (batch fan-out; imports only `core`), `go/helpers` (leaf
utilities), and `go/tests` (may import anything). No package imports a higher layer. Integration
families in a tier don't import each other (`clients/connector` is handed a storage builder rather
than importing `clients/storage`); the shared clients-tier pieces are `clients/client` (the
client tier and kind registry) and `clients/rpc/wire` (the RPC-status ↔ domain-code codec).
`.golangci.yml` enforces the layer order and the `go/helpers` leaf rule
with `depguard`; the `go/execution` and same-tier rules have no lint rule, so review holds them.

Python mirrors the tiers: `core` → `foundation` (+ `execution`) → `clients` → `repos` →
`services` → `pipelines` → `workflows` → `controllers` (the transport edge). Imports point
downward, except one type-checking-only import in `foundation/resilience/sqs_dlq.py`.

## Swappable components

A swappable component is a **`Kind`** (Go: a string `client.Kind` in the clients tier, `type Kind
int` + `iota` + `String()` in `foundation`; Python `StrEnum`), a **`Config`** carrying the kind and
backend settings, and **a factory** that fails loudly on an unknown kind:

- Go clients: each family root is one file — `Kind` constants, a data-only `Config` (mapstructure
  tags; `Name`, the client's name in the decoration config), a `Deps` with the
  runtime collaborators (observability, SDK clients, a shared Redis client), a kinds table
  (`client.Kinds[Config, Deps, T]`) and `NewFromConfig(ctx, cfg, deps)`. `Kinds.New` builds the
  backend an empty kind defaults to or `CodeInvalidInput` naming the known kinds; `Kinds.Build`
  also runs it through the client tier (`Decorate` in the family's `decorated.go`).
- Go foundation: `New(kind, opts...)` / `NewFromConfig(cfg)` (`foundation/logger`,
  `foundation/metrics`, …); an unknown kind is a `CodeInvalidInput` error.
- Python: `new_<type>_from_config(config)` (or `<type>_from_config`, e.g. `executor_from_config`);
  an unknown kind raises `ValueError` in the older tiers and a coded `AppError(INVALID_INPUT)` in the
  newer ones (`clients/audit`, `clients/facts`, `clients/token_ledger`).
- **Consumer-supplied kinds.** A backend that must call a consumer's own service (which the KE may
  not import) is injected: the factory takes `backends: Mapping[str, Callable[[Config], T]]`, and
  `Config.kind` is a `str` matched against the built-in kinds first, then the `backends` keys
  (`clients/token_ledger/builder.py`, `clients/audit/builder.py`). The kind stays a config value; the KE stays product-free.

One backend per sub-package beside the factory: `clients/storage/{s3,memory}`,
`clients/lock/{local,redis}`, `clients/cql/{cassandra,keyspaces}` (sharing the gocql adapter in
`clients/cql/driver`), `foundation/logger/{zap,stdlib}`, `clients/llm/{bedrock,ollama,stub}`.
Python imports heavy or optional dependencies lazily in the selected branch, so the `ray`,
`postgres`, `parsing` and `langdetect` extras load only when chosen (`clients/lock/builder.py`).

## Interface composition

`go/core/interfaces` holds small role interfaces (`Reader`, `Writer`, `Lister`, `Exister`,
`Lifecycle`, `HealthChecker`, …) that larger contracts (`Repository`, `Service`, `Client`) compose.
Role compositions live in `core` too (`ReadListExistStore` embeds `Reader`/`Lister`/`Exister`;
`CRUDNoListStore`, `CRUDNoListRepository`, `InsertOnlyStore`), with generated mocks in
`go/tests/mocks`. An interface declared **outside** `core` embeds a core interface
(`clients/rpc.RPCClient` embeds `interfaces.Client`). Opt-in
capabilities compose rather than widen (`DynamicConsumer` embeds `MessageConsumer`); Python
Protocols do the same (`StorageClient` composes `ManagedResource`).

The data-access roles are input-generic where a write takes a command rather than the entity:
`Creator[T, C]` creates from `C`, `Updater[T, ID, U]` updates from `U`, and `Writer[T, ID]` is the
pair whose input is `*T`. `Resource[T, P, ID, C, U]` composes `Reader`, `Lister`, `Creator`,
`Updater`, `Deleter` and `Exister`.

**Every tier builds on one base shape.** Each tier package (`clients/client`, `stores/store`,
`repos/repository`, `services/service`, `pipelines/pipeline`, `workflows/workflow`,
`transport`) has `<tier>.go` — `Tier()`, `Base` and `NewBase[C](decs, name, …) (Base, error)`;
each entity tier (store, repository, service, pipeline, workflow) also has `entity.go`, its
`Entity` base. `C` is the component's contract (an
interface): each of its methods is one operation, decorated as `<name>.<op>` where `op` is the
method's snake_case name (`FindByKey` → `find_by_key`), so the config tunes the component or one
operation. A consumer component embeds its tier's `Base`, implements each contract method through
the promoted `Exec` (and `ExecOnce`, `ExecTx`, `InTx`, `Write`, … where its tier has them) under
the method's op name, and calls the next tier. Running an op the contract does not declare fails
`INTERNAL`.

**An entity component embeds one base that serves its roles.** A Go method reaches a type only
through what the type embeds, and a method with its own type parameter satisfies no interface,
so a base that does not know the entity's types cannot have `Get(ctx, id ID) (*T, error)`. Each
entity tier therefore has an `Entity` base typed by the entity, which embeds the tier's `Base`
and serves the roles whose signatures use only `T`, `P` and `ID`: `Get`, `List`, `Create`,
`Update`, `Delete`, `Exists`, `Insert`, `Upsert` and `PurgeBefore`.

| Tier | Entity base | Built over |
| --- | --- | --- |
| store | `store.Entity[C, T, P, ID]` (`store.NewEntity[S](decs, name, raw, store.Funcs{…})`) | one statement per role, run on the statement's client; reads through `Exec`, writes through `Write` |
| repository | `repository.Entity[T, P, ID, N]` (`repository.NewEntity[R, T, P, ID](decs, name, next, cache)`) | the store `N`; reads and idempotent writes retried, `Create` never, `Insert` and `Upsert` observed only; a non-nil `*Cache` puts the read-through entity cache beneath the roles |
| service | `service.Entity[T, P, ID, N]` (`service.NewEntity[S, T, P, ID](decs, name, next, validate)`) | the repository `N`; `validate` checks every `Create`, `Update` and `Upsert` entity |
| pipeline | `pipeline.Entity[T, P, ID, N]` (`pipeline.NewEntity[C, T, P, ID](decs, name, next)`) | the service `N` |
| workflow | `workflow.Entity[T, P, ID, N]` (`workflow.NewEntity[W, T, P, ID](decs, name, tx, next)`) | the pipeline `N`; reads with no transaction, every write one command |

The contract decides the roles: the constructor serves exactly the roles its contract names and
fails `INTERNAL` at construction when one has no statement or the next tier lacks the method; a
role outside the contract is refused at call time. `Next()` returns the next tier as its contract
declares it, product verbs included. A component writes its product operations through `Exec`
and calls `Next()`; an override of a role runs under the same op name and calls `Next()`, never
the role it shadows. `foundation/decorate.Entity` is the shared core: the tier supplies a
`RoleRunner` that runs each call by its `RoleKind` (read, create, idempotent write, append).
A job is one `job.Base` per job (`NewBase(decs, name)`, its op `execute`); a gate is decorated as
the gate-tier component of its name (`Decorate(decs)`, its op `run`). A client whose operations
are wire-level names known per call (an RPC procedure, an HTTP route, a job kind) uses
`client.NewWireBase(decs, name, opts…)`, whose ops all run on the client's own chain; a
workflow's loop pass runs on its workflow's own chain, labeled by its loop (`Pass`). Tests build
any tier undecorated with `fixtures.Must(x.NewBase[C](fixtures.Decorations(), "test"))`, or over
chosen collaborators with `decorate.Fixed(deps)`. Python mirrors the shape: `XBase(contract,
decorations, name)`, `ClientBase.wire(decorations, name, *options)`, `Decorations.fixed(deps)`.

An out-of-core interface that cannot embed one carries a doc-comment tag saying why:

- `// SDK seam` — mirrors a third-party SDK surface (AWS S3/SQS/Secrets Manager,
  `grpc.ClientConnInterface`) so the adapter can be driven by a generated mock.
- `// sealed union` — a closed implementer set standing in for a sum type (`core/types.Filter`).
- `// interface-composition exemption — <reason>` — a mechanism primitive or narrow consumer-side
  port, not a data-access surface (`foundation/decorate.OpMiddleware`, `execution/gate`, …).

Every interface under `go/` outside `core` (tests and mocks aside) embeds a core interface or
carries a tag. Keep the tags verbatim.

## Dependency injection

Collaborators arrive as constructor arguments, `With*` builder calls, or functional options
(`foundation/options.Option[T]`); the Go tree has no `init()` functions or service locators (the
OTel tracer backend is the one piece that installs process-global state). KE is a library: the
consumer's `main` is the single composition root that builds config, calls the factories, applies
decorators, and registers clients with `foundation/lifecycle.Manager` (start in order, stop in
reverse). Lifecycle-managed clients connect in `Start`, not their constructors (`stores/gorm`,
`clients/database/postgres`). Python backends are `ManagedResource` async context managers; the
documented exception is logging, which structlog configures once and seams resolve by name.

## Decorators

Cross-cutting concerns wrap business logic and backends, never live inside them. A nil
collaborator or zero timeout skips its layer; recovery needs none.

- **Go — request tiers.** Pipelines, workflows, services and repositories share one shape and one
  engine. `foundation/decorate` holds every cross-cutting body once (`OpMiddleware`, `Chain`, the
  recovery, tracing, metrics, logging, auth, timeout, circuit-breaker and retry middlewares). A
  `decorate.Tier` names a tier and orders its layers as data; `Tier.Subject(name, decorate.Deps)`
  builds the chain for one subject from its collaborators (a nil one, or a zero timeout, leaves
  its layer out), and `Subject.Exec[R](ctx, op, fn)` runs an operation of any return type through
  it. The role decorators (`decorate.Reader`, `Lister`, `Writer`, `Inserter`, `Deleter`,
  `Exister`) run each method of one core role through a Subject, so every decorated CRUD shape is
  an embedding of the roles it honors. `Decorations.SubjectOf[C](tier, name)` builds a
  component's Subject from its contract: one set of chains per operation, each resolved from
  `<name>.<op>`, with views that drop layers for one call — `Once()` (no retry: a write that
  must not replay), `Observed()` (observability only: a statement inside a caller's
  transaction), `Streaming()` (no retry or timeout: a stream whose deadline its caller owns)
  and `Component()` (any op on the component's chain). An operation reuses its component's
  circuit breaker, bulkhead and rate limiter unless its own override configures that layer, so
  a limit set on the component bounds its operations together. Per tier:
  - `pipeline.Base`: `Stage[In, Out](op, fn)` builds a decorated `pipeline.Stage` (a function
    type that is an `interfaces.Pipeline`), whose `Then[C](next)` composes it with the next
    stage; `FanOut[In, Out](op, stages...)` runs stages concurrently on one input.
  - `workflow.Base` embeds the workflow's Subject over a `TransactionManager`; it is the only tier
    that opens a transaction. A consumer workflow embeds the Base and runs each operation as a
    read (`Exec`, no transaction), a command (`ExecTx`, one transaction) or a saga (`Exec` whose
    body commits each local step with `InTx`, undecorated). `ExecTx` and `InTx` refuse a context
    already in a transaction, so a transaction never opens inside another. `workflow.Entity`
    serves an entity's reads with no transaction and each write as one command.
  - `service.Base` embeds the service's Subject; `service.Entity` serves an entity's roles over
    a repository with an optional entity validator.
  - `repository.Base` embeds the repository's Subject; `ExecOnce` runs a non-idempotent write.
    In `repository.Entity` a Create is never retried, and an in-transaction `Insert` or
    `Upsert` runs only its observability.
  - `go/stores` is the store tier, between the clients and the repositories: `store.Tier()`
    (Tracing → Metrics → Logging → Timeout; unobserved by default) and `store.Base[C]` — `Exec`
    runs a statement through the tier, `Write` runs one only inside a transaction, and
    `Client(ctx)` returns the client a transaction manager bound with `store.WithClient`, else
    the store's own. The ORM is the store's kind (`stores.kind`: `ent` or `gorm`), one
    subpackage each: `stores/ent` (the list-query compiler backend, `Apply`, `FoldFilters` /
    `FoldOrder`) and `stores/gorm` (the lifecycle-managed `*gorm.DB`); `stores/errors` maps a
    driver's errors to coded ones (`postgres/`).

  Every tier's series are `<tier>_operations_total`, `<tier>_errors_total` and
  `<tier>_operation_duration_seconds` labelled `{<tier>, operation}`; its spans are
  `<name>.<op>` with `<tier>.name` / `<tier>.operation`. The clients tier uses the same engine:
  `client.Tier()` and `client.Base` (`Exec`, `ExecOnce`, `Do`, `DoOnce`, `Stream` and `Open`
  for a stream, bounded by its op's timeout), with the `Producer()`, `Handler()` and
  `AtMostOnce()` options, and each decorated family's `Decorate(base, decs, name, opts…)`, which
  `client.Kinds.Build` calls; its series are `client_*` labelled `{client, operation}`.
- **Go — every decorator is configurable.** The `decoration` config section
  (`schema/decoration.Load`) holds one `Tier` per tier — `controllers`, `clients`, `workflows`,
  `pipelines`, `services`, `repositories`, `stores`, `jobs`, `gates` — each with `defaults` and
  per-component `overrides` keyed by the component's dotted name, switching and tuning every
  layer (recovery, tracing, metrics, logging, auth, timeout, retry, circuit breaker, rate limit,
  bulkhead). An override applies to its name and every name beneath it, the deepest winning:
  `api.documents.create` gets the defaults, then `api`, `api.documents` and its own override.
  The order stays the tier's, in code. The composition root builds one `decorate.Decorations`
  from that config and the `Collaborators` (logger, metrics, tracer, authorizer) and hands it
  down; a component's `decorate.Deps` is `Decorations.Deps(<tier>.Tier(), name)`, so turning a
  layer on for one component is a config change. Every base resolves its component as `<name>`
  and each contract operation as `<name>.<op>`; `Decorations.Inventory()` lists every name
  resolved with its layers and the config keys they came from (`Sources`), for a deployable's
  `--print-decorations`. **Fail loudly:** an override that turns on a
  layer its tier does not run (retry on a workflow) is `INVALID_INPUT`, and once the graph is
  built the root calls `Decorations.Verify(prefixes…)`, which fails `INVALID_INPUT` on every
  override under those prefixes that no component resolved (a misspelled or stale key) and on
  every resolution `Decorations.Subject` recorded as failed. A deployable whose decoration file is
  generated from `Decorations.Components()` calls `VerifyExplicit` instead, which also fails on a
  component with no entry of its own name. **Observe once per request:** by default clients, workflows and
  jobs are observed and the pipeline, service, repository and store tiers are not; repositories
  retry behind a breaker; the `storage`, `outbox.*` and `prometheus` clients carry a 30 s
  timeout, bulkhead and breaker.
- **Python — the same tiers, the same config.** `foundation/decorate` mirrors Go's package:
  `OpMiddleware`, `chain`, `exec_op`, one `new_*` middleware per layer, `Tier` (layers as data,
  outermost first), `Subject`, `Deps`, and `Decorations`, which builds a component's `Deps` from
  the same `decoration` section (`foundation/config/decoration`; durations are Go strings such as
  `"30s"`, read by `foundation/config/duration.parse_duration`). Each tier package has Go's
  `tier()` and a base built from the contract, `XBase(contract, decorations, name)`, whose ops are
  the contract's public methods: `clients.client.ClientBase` (`exec`, `exec_once`;
  `ClientBase.wire(decorations, name, *options)` for wire-level ops, with `producer()`,
  `handler()` and `at_most_once()`), `repos.RepositoryBase` (`exec`, `exec_once`, `crud(store)`,
  Create never retried), `services.ServiceBase` (`exec`, `crud(repo, validate)`),
  `pipelines.PipelineBase` (`stage`, `fan_out`; `Stage.then` chains), `workflows.WorkflowBase`
  (`exec`, `exec_tx`, `in_tx`), `execution.job.JobBase(decorations, name)` and
  `controllers.ControllerBase`. Spans, series, log lines and op names match Go's. A consumer
  repository or service subclasses `DelegatingRepository` / `DelegatingCrudService` and runs its
  custom ops through the base's `exec`. The retry layer's
  `ExponentialRetrier` (and `retry_transient_async`) wait out a server pushback
  (`details["retry_after_ms"]`, read by `retry_after_s`), capped by the max interval.
- **Python — GenAI span enrichment.** `clients/decorators/ai_enricher.AiSpanEnricher` wraps an
  `LLMProvider` or `RetrievalEngine` and stamps the span already current (the step's) with
  `gen_ai.system`, `gen_ai.step`, `gen_ai.request.model` (the model asked for),
  `gen_ai.response.model` (the model that served the call), `gen_ai.usage.{input,output}_tokens`,
  `gen_ai.response.finish_reason`, or `retrieval.{top_k,result_count,document_ids}`; it opens no span.
  Both `stream` and `stream_with_usage` are wrapped (the latter re-yields its trailing
  `StreamUsage`). Around a `FallbackLlmProvider`, `gen_ai.system` names the fallback, not the member
  that served; wrap each member in its own enricher to attribute it.
  Optional content capture records `gen_ai.content.{prompt,completion}` events, PII-redacted by
  `foundation/logger/redact.redact_pii` (the Go `RedactPII` port; both suites read
  `testdata/redact_vectors.json`) and capped at 4 KiB. With an injected `MetricsProvider` it emits
  `gen_ai_tokens_total{step,model,type}` and `gen_ai_request_duration_seconds{step,model}` under the
  served model; with an injected `FactPublisher` it publishes one `genai_calls` analytics fact per
  model call (stamped with the call's start time), whose
  product dimensions come from an injected `fact_dimensions` callable (the KE names no product
  dimension). A stamping or emission failure is logged, never raised.
Inner logging layers (client stack, repository, service, pipeline, workflow) log failures at Debug
so the edge logs the error once: the Controller tier is the edge (`Tier.Edge`), so its logging
layer logs a failure at Warn for a client fault (HTTP status below 500) and at Error otherwise, and
the Connect and gRPC servers' access-log interceptors stand in for it. Recovery logs a panic with
its stack even where the logging layer is off (`Deps.PanicLogger`). The lock and replay-buffer
loggers still log at Error.

### Decorator order

Outermost → innermost:

| Stack | Where | Order |
| --- | --- | --- |
| Client | `go/clients/client` (`Tier`; each family's `Decorate`) | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging — decorated: storage, messaging publish, lock, replay buffer, analytics store, connector source, and the SDK/HTTP seam of the outbox sinks (SDK faults coded so only throttling/server faults retry) and the Prometheus querier (op `prometheus.<endpoint>`; the capped body is read inside the attempt; 429/502/503/504 transient) |
| Message handler | `go/clients/messaging/{sqs,redis}` | the Client tier as `<name>.handler` (`client.NewWireBase` with `client.Handler()`) in CONSUMER spans, each publish in a PRODUCER span (`client.Producer()`; a Redis publish, at-most-once, is never retried — `Kinds.AtMostOnce`): a handler is never retried, broken or bulkheaded — the broker's redelivery is the retry. The publisher's trace context (`tracer.Inject` / `Extract`) rides SQS message attributes and memory-broker metadata, so the handler continues the publisher's trace; Redis Pub/Sub carries the payload alone, so a trace stops at a Redis channel |
| Controller | `go/transport` (`Tier`; `Base.Handler` for a REST/RPC handler) | Tracing (server span) → Metrics → Logging → Recovery → RateLimit → Bulkhead (sheds when `bulkhead.shed`, the controllers' default) → Auth (`<controller>.<op>`) → Timeout — switched and tuned by `decoration.controllers` |
| Connect server | `go/transport/connect/interceptors.ServerOptions` | the Controller tier via `transport.Interceptors`, op `pkg.Service/Method`: Connect-native Tracing (continues the caller's trace, sets `traceresponse`), Metrics (`connect_rpc_*`, by status code) and Logging (status-aware access line) stand in for the tier's; `Edge.Authn` (service auth, gateway-header auth, the consumer's principal and tenant-scope interceptors) runs just before Auth; `Edge.Inner` (retry budget, validation) after the tier. A stream gets no timeout; a handler's error is returned verbatim, a tier rejection becomes a `*connect.Error` of its code |
| gRPC / Connect client | `go/clients/rpc/grpc/interceptors.ClientOptions`, `go/clients/rpc/connect/interceptors.ClientOptions` (installed by `rpc.NewFromConfig`) | the Client tier under the client's name, op `pkg.Service/Method`; per attempt the trace context (and, gRPC, the service token) rides the outgoing metadata. A server status is coded by `clients/rpc/wire` for the tier and returned verbatim; a tier-made failure becomes the status of its code. A gRPC stream's open runs through `client.Base.Open` (never retried; the timeout bounds only the open) |
| gRPC server | `go/transport/grpc/interceptors.ServerOptions` | the Controller tier as for Connect, op `pkg.Service/Method`, for unary and stream RPCs: gRPC-native Tracing (continues the trace from the incoming metadata), Metrics (`grpc_server_*`) and Logging stand in for the tier's; `Edge.Authn` / `Edge.StreamAuthn` run just before Auth. A stream gets no timeout; a handler's error is returned verbatim, a tier rejection becomes the sanitized status of its code (`wire.SanitizeGRPC`) |
| Repository | `go/repos/repository` (`Tier`) | Tracing → Metrics → Logging → Timeout → CircuitBreaker → Retry (→ `Cached` beneath) |
| Store | `go/stores/store` (`Tier`; `Decorate`, `Base`) | Tracing → Metrics → Logging → Timeout |
| Service | `go/services/service` (`Tier`) | Recovery → Tracing → Metrics → Logging → Authorization → Timeout (a CRUD validator runs inside) |
| Pipeline, Workflow | `go/{pipelines/pipeline,workflows/workflow}` (`Tier`) | Recovery → Logging → Tracing → Metrics → Timeout |
| Execution job | `go/execution/job` (`Tier`) | Recovery → Logging → Tracing → Metrics → Timeout |
| Python tiers | `clients/client.py`, `controllers/handler.py`, `repos/repository.py`, `services/service.py`, `pipelines/pipeline.py`, `workflows/workflow.py`, `execution/job/base.py` (each `tier()`) | the Go order of the same tier, switched and tuned by the same `decoration` section |
| Python gRPC server | `controllers/grpc/interceptors.server_interceptors` (`controllers.interceptors.lay_out`, the `transport.Interceptors` twin) | the Controller tier, op `pkg.Service/Method`, as `grpc.aio` interceptors: gRPC-native Tracing (continues the caller's trace, sets `traceresponse`), Metrics (`grpc_server_*`) and Logging stand in for the tier's; `Edge.authn` (`service_auth`, `AuthServerInterceptor`) runs just before Auth and `Edge.inner` (`ValidatingServerInterceptor`) last. Every RPC shape runs as one coroutine (a stream written with `context.write`), so the tier wraps a whole stream, which gets no timeout. A handler's own status passes unchanged; a coded error becomes the status of its code |
| Python gRPC client | `clients/rpc/grpc/interceptors.client_interceptors` (installed by `new_rpc_channel_from_config`) | the Client tier under `RpcConfig.name`, op `pkg.Service/Method`; per attempt the trace context and `RpcConfig.service_token` ride the outgoing metadata. A server status is coded by `grpc_error_to_app_error` for the tier and returned verbatim; a tier-made failure becomes an `AioRpcError` of its code. A server stream only carries the metadata (`grpc.aio` opens it lazily) |
| Python messaging | `clients/messaging` (`decorate`; `new_messaging_from_config`, `new_messaging_subscriber_from_config`) | as Go: each publish runs through the Client tier under `MessagingConfig.name` as op `<topic>`, each handler call through the handler tier as `<name>.handler` (`ClientBase.handler`; never retried, broken or bulkheaded), in PRODUCER and CONSUMER spans (`ClientBase.producer` / `ClientBase.handler`). The publisher's trace context (`foundation/tracer.inject` / `continued`) rides SQS message attributes and memory-broker metadata, so the handler continues the publisher's trace. A memory publisher and subscriber connect through the `broker` both factories take |

Each tier is a `decorate.Tier` (its name, span kind and `Order`); `Tier.Chain` lays a component's
`Deps` out in that order, leaving out a layer whose collaborator is absent. A server's interceptors
walk the same `Order` through `transport.Interceptors`, standing in a protocol-native interceptor
for tracing, metrics and logging. In the client tier Retry sits outside the breaker, so each attempt
is observed; a non-idempotent op runs on `Deps.WithoutRetry`, and `client.Base.Open` (a stream's
open) is never retried. Custom repository and service ops (`Base.Exec`) run the same chain as their
tier's CRUD. Tests pin each tier's order in its `<tier>_test.go` (`clients_client_test.go`,
`transport_controllers_test.go`, …), `foundation_decorate_test.go` proves `Tier.Chain` applies an
order, and `transport_server_options_test.go` drives the Connect and gRPC servers end to end.

### Conversation threading

A multi-turn conversation is one trace per turn, grouped by id and linked turn to turn. The edge
that owns the conversation puts three W3C baggage members on each turn (Go
`foundation/tracer.InjectConversation`): `vv.conversation.id` (opaque, `^[A-Za-z0-9_-]{1,64}$`),
`vv.turn.index` (0-based) and `vv.prev.traceparent` (the previous turn's root span). The turn's
root span carries the attributes `conversation.id` and `turn.index` and one span link to the
previous root (Go `ConversationStartOptions`; Python `foundation/tracer/conversation`:
`extract_conversation`, `stamp_conversation`, `conversation_links`). A missing or malformed id or
index yields an unthreaded trace; a malformed `traceparent` drops only the link.

## Query core (listquery)

List endpoints and charts share one query core in `foundation/listquery`. A `listquery.Schema`
names a field allow-list (`*listquery.Map`) plus, for a chart cube, its drill hierarchies and
grains. A filter is a `core/types.FilterNode` tree whose JSON is the operator-first grammar
(`{"$eq": {"field": v}}`, `{"$and": [...]}`, `{"$or": [...]}`; nine operators, depth ≤
`types.MaxFilterDepth`). `ParseFilter` checks a tree against the `Map` and returns a
`types.Filter`; `Parse` decodes the grammar string and calls `ParseFilter`, so a structured
client and a JSON client get the same filter. `ParseSort` reads `field[:asc|desc]` tokens and
accepts only fields declared `.Sortable()`; an unknown, unsortable or repeated field or a bad
direction is `CodeInvalidInput`. `testdata/listquery/filters.golden.json` is the shared corpus
both decoders are tested against.

## Analytics (ChartSpec)

`core/types.ChartSpec` is a declarative chart: Rows/Columns shelves of table-algebra expressions
(`concat`, `cross`, `nest`) over one cube's fields, plus detail, encodings, a listquery filter
tree (`FilterTree`, wire key `filter`), sort, time range, grain and an optional mark.
`foundation/chartspec` is the pure compiler (no I/O): `Parse` validates the JSON spec against the
cube's `listquery.Map` and compiles the filter tree with `listquery.ParseFilter`; `Compile` and
`Drill` take the cube's `listquery.Schema` (a field's `Role` — dimension,
measure or time — and the `Aggregates` a measure permits; a sort key must name a placed dimension or
aggregated measure; depth ≤ 8; every rejection `CodeInvalidInput`), `Normalize` builds the tuple
table, `Compile` derives one `AggregateQuery` plus the panes and a mark per pane (O×O text, O×Q bar,
T×Q line, Q×Q point), `Drill` swaps a dimension for its hierarchy child (sort keys included), and
the reducer merges `Partial`s (sum, count, min, max and a 1%-accurate DDSketch for p50/p95/p99).
Facts travel as `core/types.Fact`, whose JSON is byte-identical to Python's `core/types/fact.Fact`
(both suites assert `testdata/analytics_fact.golden.json`).

A spec is versioned as content-addressed nodes. `Encode` splits it into a `Tree` of `Node`s
(`kind`, scalar `fields`, single `children` and ordered `lists` of child hashes); a node's `Hash`
is the SHA-256 of `"chartspec/v1\x00"` followed by its RFC 8785 canonical JSON. Equal subtrees
share one node, so changing one aggregate re-addresses only the nodes on its path to the root.
`Decode` rebuilds a spec from a root through a caller-supplied `NodeGetter` and re-hashes every
node it reads (missing → `CodeNotFound`, content not matching its address → `CodeInternal`);
`Diff` compares two roots and returns the changed paths (`columns.args[0].agg`), descending only
where hashes differ. `testdata/chartspec/*.golden.json` pins the node and root hashes.

`core/interfaces.AnalyticsStore` (`Aggregate` → a pull-based, page-at-a-time `RowStream`; idempotent
`Write`; optional `AnalyticsCompactor`) is built by `clients/analytics` (`KindStub` default,
`KindCassandra`). The Cassandra store sits on `core/interfaces.CQLSession`, built by `clients/cql`
— `cassandra` (self-managed, token- and DC-aware) or `keyspaces` (Amazon Keyspaces: TLS + SigV4) —
and keeps
to the CQL both share: one table per cube and grain, partition `((org_id, cube, bucket))`,
clustering `(ts, dims_key, idempotency_key)`, one partial row per fact (a redelivery overwrites it),
dimension filters applied in Go (time clauses also narrow the clustering range; `OpLike` is a
case-insensitive substring match with `%` and `_` literal), and compaction in single-partition
logged batches of at most 30 statements. `NewFromConfig` does no I/O: the Cassandra store is built
by `NewLazy` and dials at `Start` (an operation before it is `CodeUnavailable`). With `EnsureSchema`,
`Start` also creates every declared table (`CREATE TABLE IF NOT EXISTS`, once) and, on Keyspaces,
polls `system_schema_mcs.tables` until each is `ACTIVE`, failing `CodeUnavailable` after
`SchemaTimeout` (default 2 min) and closing the session so a retried `Start` redials. Keyspaces defaults
to port `DefaultPort` (9142) and loads the AWS credentials chain on first use. A stream's first read
error is terminal, a resume token is bound to the query that issued it, and a compacted row keeps
the bucket's retention: it expires at bucket end + the grain's TTL (CQL `TTL()` cannot be read off a
non-frozen map, so the TTL is anchored to the bucket, not to each fact's write).

The transactional outbox is split by tier: the contracts in `core`, the sink kinds in `clients`,
the store kinds in `stores`, the relay in `workflows`. `core/interfaces.OutboxStore` (`Enqueue`, the producer side
`core/interfaces.OutboxWriter`, writes a pending row on the transaction bound to the context and
refuses to run without one; `Claim` leases due rows, `MarkSent` / `Retry` / `Park` / `Stats`, `DeleteSentBefore` for the retention sweep) is
built by `stores/outbox` (`KindStub` default — no rows, so a relay boots with no database;
`KindPostgres`, a product's own table named by config, whose writes run only on the `*sql.Tx` that
`stores/sqltx` — the `database/sql` transaction manager, which refuses a nested transaction —
bound to the context) behind the store tier, and
`repos/outbox.New` runs it through the repository tier as the
`core/interfaces.OutboxRepository` the relay takes (`Claim` without retry, since it leases rows
and counts an attempt; the idempotent finalizers, `Stats` and the sweep with it);
`core/interfaces.OutboxSink` returns one result per record. `workflows/outbox.Relay.RunOnce` is a
saga: it claims a batch in one transaction, sends it in `SendBatch` chunks under a `Concurrency`
bound with no transaction open, and finalizes each row in its own transaction by its own outcome:
sent, retried at `now + FullJitter(min(Base·2^attempts, Max))`, or parked on its `MaxAttempts`th
attempt (`outbox_{sent,retried,parked}_total`, `outbox_depth`, `outbox_lag_seconds`
by lane). With `ParkOnPermanent` a permanent failure parks the row at once; a run cancelled mid-send
leaves its unfinished rows leased for the next claim. `LoadRelayConfig` reads a lane's `RelayConfig`
over the defaults. `clients/outbox` builds the sink (`KindStub` default, `KindSQS`, `KindS3`) over
an injected SDK client: SQS routes each row by its `route` attribute through a config route → queue
map with per-queue failure isolation (a `.fifo` queue gets group id `Key`, else `Tenant`, else
`Lane`, and deduplication id `ID`); the sinks keep an SDK error's code (`errors.CodeOr`); S3 writes
one object per row by key template with a SHA-256 checksum (Object Lock) and optional SSE-KMS.
`go/tests/fixtures/outboxtest` is the store and routing conformance suites (`Run`, `RunRouting`),
run against the `postgres` store kind.

`core/interfaces.MetricsQuerier` evaluates PromQL: `Query` returns one instant sample and
`QueryRange` one series of `core/types.MetricSample`. It is built by `clients/prometheus`
(`KindStub` default, which answers 0 and an empty series; `KindHTTP`, the Prometheus HTTP API
client). The HTTP kind fails closed:

- no sample, or a NaN or infinite one, is `CodeNotFound`;
- several series, the wrong result type, a bad range (step <= 0, end before start), a rejected
  query (HTTP 400/422) or a body over `MaxBodyBytes` is `CodeInvalidInput`;
- a transport failure or a throttling/unavailable status (429/502/503/504) is
  `CodeUnavailable`, which the client stack retries and its breaker counts;
- the caller's own deadline is `CodeTimeout` and its cancellation `CodeCanceled`;
- any other error status, a `"status":"error"` envelope or an unparsable body is
  `CodeUpstream`.

The stub is the deliberate exception: it measures nothing and answers zero, so the graph boots
without a metrics server.

## Configuration

Config selects; the composition root injects. Go `foundation/config` (Viper) layers `base.yaml` →
`{env}.yaml` → `secrets.yaml` → env vars into typed structs (`foundation/config/schema/*`, each with
`Default…Config()` and a coded-error `Validate()`). The consumer owns its conventions: the env
prefix, its root config struct (`WithSchema` — every leaf field binds `<PREFIX>_<PATH>` plus its
`envalias` names) and extra bindings for keys outside that struct (`WithExtraEnv`).
`WithEnvSelectors` (over `viper.ResolveEnvFrom`) picks the overlay from the consumer's own env vars
(default `APP_ENV`, then `ENVIRONMENT`): the first one set, trimmed and lowercased; with none set,
`base.yaml` loads alone. Set an env prefix: unprefixed names collide with the service-link
variables Kubernetes injects (`REDIS_PORT=tcp://…`). Without `WithSchema`, `UnmarshalKey` sees env
overrides only for keys present in the YAML or listed in `WithExtraEnv`. A `WithExtraEnv` entry
replaces that key's schema-derived binding, so its `envalias` names stop applying;
`<PREFIX>_<PATH>` still takes precedence through Viper's AutomaticEnv.

Python `foundation/config` loads the same layers into pydantic-settings classes:
`initialize_config(env_prefix=..., env_selectors=...)` exports the YAML under the consumer's
prefix (given with or without its trailing `_`), and the consumer's settings class sets the
matching `env_prefix`; `strict=True` raises on a missing directory, a missing `base.yaml` or bad
YAML instead of warning. Overlay selection differs from Go: Python uses the first set selector's
value as-is (not lowercased) and falls back to `dev` (`dev.yaml`) where Go loads `base.yaml`
alone, so a mixed Go/Python stack should set the selector explicitly.

Business logic never reads the environment. In `go/`, env reads are confined to the config loader,
the `env` secrets backend, subprocess plumbing in `foundation/system`, `helpers.OrDefault`, and one
documented exception: the logger's deploy-time `GIT_SHA` (mirrored in Python).

## Error codes

Every error crossing a package boundary carries an `ErrorCode`. Go `go/core/errors` defines the
codes (`NOT_FOUND`, `INVALID_INPUT`, `UNAUTHORIZED`, `FORBIDDEN`, `CONFLICT`, `TIMEOUT`,
`UNAVAILABLE`, `UPSTREAM`, `RESOURCE_EXHAUSTED`, `INTERNAL`, …); `New(code, msg)` / `Wrap(err, code, msg)` build an
`*AppError` keeping cause and origin stack; `Code(err)` / `Is(err, code)` read it. Production code
under `go/` has no `fmt.Errorf`; `.golangci.yml` bans it via `forbidigo`.

Classification is derived from the code, never the message: `IsTransient` / `IsPermanent`, the
retry policy (`foundation/resilience/retry/exponential.IsRetryable`), the circuit breaker (permanent
domain errors count as successes), and the edges (`ToHTTPStatus`; `transport/rpc.ToConnectError`,
which maps codes and captures the real cause for the request log). Python raises
`AppError(code, message, cause=…)` or a subclass from `core/errors`; `is_transient`, `grpc_status`
and `http_status` derive from the code, retries retry only transient `AppError`s, and boundary
helpers translate SDK errors (`foundation/resilience/grpc_boundary.py`,
`foundation/resilience/aws_boundary.py`). `RESOURCE_EXHAUSTED` (a spent quota, HTTP 429 / gRPC 8) is
neither transient nor permanent, so no retry layer in either language retries it. The committed
`go/core/errors/testdata/codes.json` pins every code's `{grpc, http, transient, permanent}` row; the
Go suite regenerates it and the Python suite compares against it.

## Stub-first backends

Most tiers ship an in-process, stub, or no-op backend, so wiring needs no cloud dependency:

- **Go:** analytics `stub`; lock `local`; outbox sink and outbox store `stub`; messaging, replay buffer, storage `memory`; secrets
  `env`/`file`; cache, prometheus, transport `stub`; tracer `noop`; metrics no-op when disabled;
  logger `stdlib`. `database` and `rpc` default to `postgres` and `grpc`: their consumers hold the
  `*sql.DB` or connection itself.
- **Python:** cache `local`/`null`; email `noop`; audit, embedding, facts, llm, retrieval, kb_ingestion,
  token_ledger, vector `stub`; jobs, lock, messaging, storage `memory`; tracer, metrics `null`;
  executor `asyncio`.

Go cache (Redis), database (Postgres) and connector (S3) have no stub and are covered by
testcontainers integration tests. `tests/unit/clients_all_stubs_no_network_test.py` builds each
Python stub kind with every real backend constructor patched to fail.

## What belongs here

Generic substrate only: contracts, mechanisms, and adapters any service stack can reuse, including
domain-adjacent building blocks (knowledge-base, retrieval, embedding and LLM clients; RAG pipeline
stages whose prompts and policies the consumer injects). A consumer plugs its own policy into
seams: its event types into an event registry, its principal type into the principal resolver,
its config sections and env conventions into the config loader.

Product code stays with its consumers: domain event catalogs, authorization models, business
rules, prompts, schemas, storage-key layouts, deployables and deployment config.

The repository is self-contained. Consumers may reference it; it never references them — no
consumer names, paths, design documents or tracker ids in code, comments, tests or config.

## Execution substrate

Go `go/execution` models batch work: jobs fan out units, gates run jobs in phases.

- `engine`: `Pool(n).FanOut` runs over items with at most `n` in flight (a slot is acquired before
  each goroutine spawns), recovers panics into `Fail` results, never lets a unit cancel its
  siblings; `RunJobGroup` runs `AnyJob`s concurrently, streaming `StepResult`s to an
  `ExecutionObserver`; `Steps.Serial` runs plain function `Step`s in order.
- `job`: `job.Base` builds every `AnyJob` — `Func` (a function of runner and root), `Runnable`
  (runner-less in-process work) and `Wrap` (an existing job) — and decorates each once with
  `job.Tier()`.
- `gate`: `gate.New(name).Parallel(…).Serial(…).StopOnFailure().Observe(obs)`; `Run` runs the
  phases in order and aggregates failures into a `CodeQualityFailed` error, and `RunLocal` runs a
  gate of runner-less jobs.

Python `execution` mirrors these (`fan_out`, `run_job_group`, `new_gate`, `run_gate`) and adds the
`Executor` seam. `executor_from_config` chooses `AsyncioExecutor` (default) or `RayExecutor` over
the `RayRuntime` seam (the lazily imported `RealRayRuntime`, wrapped by the `ResilientRayRuntime`
retry decorator); `PooledExecutor` (a fixed pool of build-once workers) has no kind, so the caller
builds it directly.

## Background loops

Go `go/workflows/loop` runs a service's background work (outbox relays, cache refreshers,
stream drainers, retention sweeps) as workflow passes, so each pass gets the same decorator stack
as a request.

- `Pass(fn)` adapts one iteration, a `func(context.Context) error`, into a
  `Workflow[struct{}, struct{}]`. The composition root decorates it with `workflow.Base.Pass`, a
  `loop.Decorator`: the pass is one decorated operation with no transaction, and a pass that
  writes commits each write with `InTx`.
- `NewTicker(name, pass, Config, logger)` runs the pass once per `Interval`, plus one at start when
  `RunAtStart` is set. A zero `Interval` runs passes back to back, for a pass that already blocks
  on its source. A failed pass is logged at warn and followed by `ErrorBackoff`. A failure caused
  by the shutdown is not logged, and `Run` returns nil when its context ends.
- `Group` (an `interfaces.Lifecycle`) starts every `Runner` (a `Ticker`, or any `RunnerFunc`) on
  its own goroutine. A runner that returns an error is logged at error and the others keep
  running. `Wait` blocks until all have returned; `Stop` cancels them and waits within its
  context.

## Staged pipelines

Go `first.Then(second)` (a method on `pipeline.Stage`) composes two pipelines into one, feeding
first's output to second; an error from first stops the chain and is returned unchanged. Build
each stage with `pipeline.Base.Stage` before composing, so every stage records its own span and
metrics and a slow or failing stage is visible by name. `Then` chains: `a.Then(b).Then(c)` is a
three-stage pipeline. `Base.FanOut(name, stages...)` runs stages concurrently on one input and
returns their outputs in order; the first error cancels the rest. Python's
`PipelineBase.stage(name, fn)` and `Stage.then(next)` are the same, and `PipelineBase.fan_out`
raises the first failing stage's own error.

## Testing

- Go tests are black-box under `go/tests`: `unit/` (external `_test` packages), `integration/`
  (`//go:build integration`, real dependencies via testcontainers), `fixtures/` (container fixtures
  in `fixtures/dbtest/*`, suites, an E2E telemetry kit), and `mocks/`. The one white-box exception
  is `go/clients/jobs/river/runtime_internal_test.go`.
- Unit tests mock only the external boundary, with mocks **generated** by mockgen from
  `go/tests/mocks/generate.go` (`go generate ./go/tests/mocks/...`). The package is importable:
  it mocks every `core/interfaces` port a consumer's unit tests drive, so a consumer imports
  `go/tests/mocks` instead of regenerating the engine's interfaces.
- No hand-rolled fakes replace a dependency an integration test exercises; stubs are shipped
  code, not test doubles. Python unit tests use `unittest.mock`, and `tests/integration`
  (`@pytest.mark.integration`) runs against testcontainers.
- CI (`.github/workflows/ci.yml`): vet, golangci-lint, `go test -race`; ruff, basedpyright, pytest
  (90% unit coverage gate); an integration job for both languages. The combined unit + integration
  coverage config (`python/techai_webutils/.coveragerc.combined`) is an optional local gate; no CI
  job loads it.

## Versioning and releases

The Go module and the Python package version independently, and a merge to `main` releases both;
nobody pushes a release tag by hand. `.github/workflows/release.yml` runs `scripts/release.sh
release` on every push to `main`:

- **Go:** the newest released `## [X.Y.Z]` section of `CHANGELOG.md` is the module version. When
  `vX.Y.Z` is not tagged yet, the merge commit is tagged `vX.Y.Z` (what Go consumers pin) and its
  GitHub release is created from that section, marked latest.
- **Python:** the `version` in `python/techai_webutils/pyproject.toml`. When
  `techai-webutils-vX.Y.Z` is not tagged yet, the package is built and published to PyPI, then the
  commit is tagged and released. The publish runs first, so a failed upload tags nothing and the
  next run retries it.

A release PR adds the `CHANGELOG.md` section and, when the Python package changed, bumps its
version by hand. CI's `release-check` job (`scripts/release.sh check`) fails a PR that changes the
package's `src/` or `pyproject.toml` without a bump, or whose Go or Python version is not newer
than every existing tag (and, for Python, every version on PyPI, where 0.1.0–0.3.3 belong to an
earlier release line). Merge with a merge commit, not a squash, so a tagged commit stays on `main`.
