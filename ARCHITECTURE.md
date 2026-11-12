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

Outside the ladder: `go/execution` (batch fan-out and the operations registry; imports `core`
and `foundation`), `go/stdx` (leaf utilities), and `go/tests` (may import anything). No
package imports a higher layer. Integration
families in a tier don't import each other (`clients/connector` is handed a storage builder rather
than importing `clients/storage`); the shared clients-tier pieces are `clients/client` (the
client tier and kind registry) and `clients/rpc/wire` (the RPC-status ↔ domain-code codec).
`.golangci.yml` enforces the layer order and the `go/stdx` leaf rule
with `depguard`; the `go/execution` and same-tier rules have no lint rule, so review holds them.

Python mirrors the tiers: `core` → `foundation` (+ `execution`) → `clients` → `repos` →
`services` → `pipelines` → `workflows` → `controllers` (the transport edge). Imports point
downward, and no client family imports another: a family that needs a sibling's object takes it
injected (the qdrant retrieval kind and the vector knowledge base take an embedder and a store;
the passthrough model gateway takes a `provider_factory`). `uv run lint-imports` enforces both
rules (import-linter, configured in `python/techai_webutils/pyproject.toml`, run in CI),
indirect imports included. Pieces several families share live below them: `core/types/llm`
(`LlmKind`, `LlmConfig`), `foundation/tokens` (token cost and budget policy) and
`foundation/vectors` (the embedder/store dimension check).

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
  an unknown kind raises a coded `AppError(INVALID_INPUT)` (`AppValueError`). The `App*Error` classes
  named after a builtin are coded only, not subclasses of that builtin; catch them by their coded
  base.
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
(`clients/rpc.Client` embeds `interfaces.Client`). Opt-in
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
operation. A consumer component embeds a pointer to its tier's `Base`, implements each contract method through
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
A Python gRPC server's `main` serves through `controllers/grpc/serve.serve_until_signalled`:
it starts the `grpc.aio` server and the `controllers/health` app (`/healthz`, `/readyz` with an
injected dependency probe, `/metrics`) on its own port, and on SIGINT or SIGTERM flips readiness
to 503 before draining the server within its grace, as Go's `transport` server sheds readiness
before it drains.

## Decorators

Cross-cutting concerns wrap business logic and backends, never live inside them. A nil
collaborator or zero timeout skips its layer; recovery needs none.

- **Go — request tiers.** Pipelines, workflows, services and repositories share one shape and one
  engine. `foundation/decorate` holds every cross-cutting body once (`OpMiddleware`, `Chain`, the
  recovery, tracing, metrics, logging, auth, timeout, circuit-breaker and retry middlewares). A
  `decorate.Tier` names a tier and orders its layers as data; `Tier.Subject(name, decorate.Deps)`
  builds the chain for one subject from its collaborators (a nil one, or a zero timeout, leaves
  its layer out), and `Subject.Exec[R](ctx, op, fn)` runs an operation of any return type through
  it. The role decorators in `foundation/decorate/role` (`role.Reader`, `Lister`, `Creator`,
  `Updater`, `Inserter`, `Deleter`, `Exister`, `Suggester`) run each method of one core role
  through a Subject, so every decorated CRUD shape (`role.Resource`) is an embedding of the roles
  it honors. `Decorations.SubjectOf[C](tier, name)` builds a
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
    `FoldOrder`) and `stores/gorm` (the lifecycle-managed `*gorm.DB`); `stores/storeerr` maps a
    driver's errors to coded ones (`postgres/`).

  Every tier's series are `<tier>_operations_total`, `<tier>_errors_total` and
  `<tier>_operation_duration_seconds` labelled `{<tier>, operation}`. Python's metrics layer also
  counts `<tier>_throttled_total`, an error the provider refused for rate or quota
  (`foundation/resilience/classify.is_throttled`: an AWS throttle code such as S3 `SlowDown`, gRPC
  `RESOURCE_EXHAUSTED`, HTTP 429, found through the cause chain); the Go layer does not yet. Its spans are
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
  timeout, bulkhead and breaker, and the `llm` client a 120 s timeout, bulkhead, breaker and retry.
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
- **Python — usage tap.** `clients/decorators/usage_tap.UsageTapProvider` (`tap_usage(inner,
  record)`) is an `LLMProvider` decorator that hands each call's terminal `StreamUsage` to an
  injected callback: a completion's at once, a stream's once it is exhausted (`stream` re-yields
  only the text). The model is the route decision's catalog id when a gateway served the call.
  It is for a caller that sees only the answer's text yet needs the model and tokens behind it.
- **Python — token accounting.** `clients/decorators/token_accounting.TokenAccountingProxy` is the
  outermost AI-band decorator over an `LLMProvider`, `ModelGateway` or `EmbeddingProvider`: it reads
  the request's `UsageScope` from an injected `UsageScopeProvider` (no scope → passthrough), asks
  `TokenLedger.check_budget` before the call and applies `foundation/tokens/policy.decide`
  (`shadow` serves and counts `ai_budget_would_exceed_total`; `soft` and `hard` reject an
  exhausted budget with `QuotaExceededError` before the inner call; a ledger error — raised, or a
  `LEDGER_UNAVAILABLE_REASON` decision — serves under `soft` and rejects under `hard`, unless
  `on_ledger_error` overrides; with `BudgetPolicy.per_budget` an exhausted budget is enforced at
  the level its owner stored, `BudgetDecision.enforcement`, which the Redis ledger reads from the
  limits hash's `enforcement` field), and records one priced `UsageRecord`
  after it (a stream at its terminal `StreamUsage`; a semantic-cache hit, flagged through
  `core/interfaces/semantic_cache.served_from_cache()`, as one zero-token `<step>.cache_hit`).
  Recording never raises. Metrics: `ai_tokens_total{org,workspace,model,type}`,
  `ai_cost_usd_total{org,workspace,model}`.
- **Python — audit proxy.** `clients/decorators/audit_proxy.AuditProxy` runs the input
  `SafetyCheck` on the last user message (`block` → audited `blocked`, then `FORBIDDEN` before the
  model; `redact` → the model gets the redacted text), the call, the output check (`redact` →
  redacted answer; `block` → audited, then `FORBIDDEN`), and appends one `AuditRecord` built from
  the injected `AuditContext`, the live trace id and the route decision's model. Streams audit on
  exhaustion (an output block is `flagged_after_stream`); `audit_cached` is the semantic cache's
  `on_hit` hook, run before a hit is served (`cache_hit=True`; an input `redact` records the
  redacted prompt; an output `block` raises `FORBIDDEN`, a `redact` returns the redacted answer to
  serve). A record takes the route decision's model only when the audited call itself set that
  decision. `AuditEnforcement` `shadow` counts
  `audit_record_failures_total` and serves; `enforced` raises (a coded error keeps its code, else
  `INTERNAL`).
- **Python — semantic cache proxy.** `clients/semantic_cache/proxy.SemanticCacheProxy`
  is a read-through decorator around one bound method (`SemanticCacheBinding`: the unary `method`,
  optionally a `stream_method`, and how arguments map to a query and a `CacheNamespace`). A hit
  returns the decoded answer (a stream replays the cached text, then a zero-token `StreamUsage`)
  without calling the inner and sets `served_from_cache()`; a miss writes the answer through (a
  stream only once fully consumed). A coded `AppError` from the `on_hit` hook refuses the hit and
  propagates; every other cache, hook or telemetry failure is swallowed and counted;
  `enabled=False` or a `StubSemanticCache` is a passthrough. Metrics:
  `semantic_cache_{lookups,hits,misses}_total` and `semantic_cache_saving_usd_total`, each
  labelled `{org_id,path,workspace}` (`org_id` from `fact_dimensions`, empty without it; hits
  add the serving `tier`), so a hit rate is hits over lookups per org; one `cache_lookups` fact
  per lookup. A hit is counted only when it is served: a hit the hook or the caller refuses, or
  whose entry does not decode, counts as a miss with no saving. The read, serve, refuse and
  write steps live in `clients/semantic_cache/reader.
  SemanticCacheReader`, which the proxy composes and a workflow-level cache drives by hand; an
  injected `CacheLookup` (`lookup=`) selects the match tiers, else the backend's own `lookup` at
  `min_similarity` is used.
- **Python — per-key rate limits.** `core/interfaces/rate_limiter.PerKeyRateLimiter`
  (`allow_key(key)`, async) has two kinds behind `foundation/resilience/per_key_builder`:
  `memory` (`InMemoryRateLimiter`, per replica; the default) and `redis`
  (`RedisPerKeyRateLimiter`: a fixed window, `INCR` + `EXPIRE` in one pipeline, shared by every
  replica; a Redis error is `UNAVAILABLE` for the caller's policy). Through `allow_key`,
  `max_per_window` 0 is unlimited; `InMemoryRateLimiter.allow` keeps 0 as "allow nothing".
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
| Client | `go/clients/client` (`Tier`; each family's `Decorate`) | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging — decorated: storage, messaging publish, lock, replay buffer, analytics store, connector source, the LLM client (op `<name>.complete`), and the SDK/HTTP seam of the outbox sinks (SDK faults coded by `foundation/awserr` so only throttling/server faults retry) and the Prometheus querier (op `prometheus.<endpoint>`; the capped body is read inside the attempt; 429/502/503/504 transient) |
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
| Python service health | `controllers/health.start_health_server` | the HTTP surface beside a gRPC port: `/healthz` liveness, `/readyz` gated on a drain flag and a dependency probe (503 with the failed names), `/metrics`; requests run under the aiohttp observability middleware |
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
Facts travel as `core/types.Fact`; `Fact.Wire()` renders the wire form, byte-identical to Python's
`core/types/fact.Fact.to_json` (both suites assert `testdata/analytics_fact.golden.json`). Plain
`json.Marshal` of a `Fact` is not the wire form.

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
refuses to run without one; `Claim` leases a partition's due rows, `MarkSent` / `Retry` / `Park` / `Stats`, `DeleteSentBefore` for the retention sweep) is
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
leaves its unfinished rows leased for the next claim. With a `DrainBudget` (default 0: one claim per
run) a run claims again while claims come back full, until the budget elapses, the context ends or a
pass fails, and `outbox_relay_passes` records the claims per run by lane. A lane splits into
`Partitions` (default 1): the postgres store's optional `PartitionColumn` holds
`core/types.OutboxPartition(key, partitions)` = `fnv32a(key) mod partitions`, written at insert
and never recomputed, and a relay with `Partition` p claims only partition p, so one relay per
partition (each with its own leader election) drains a lane in parallel. `LoadRelayConfig` reads
a lane's `RelayConfig` over the defaults. `clients/outbox` builds the sink (`KindStub` default, `KindSQS`, `KindS3`) over
an injected SDK client: SQS routes each row by its `route` attribute through a config route → queue
map with per-queue failure isolation (a `.fifo` queue gets group id `Key`, else `Tenant`, else
`Lane`, and deduplication id `ID`; a standard queue named in `FairQueues` is an SQS fair queue and
gets the same group id with no deduplication id, so one group's backlog does not delay the others;
a non-empty `Key` also rides every message as the `partition_key` attribute); the sinks keep an SDK error's code (`errors.CodeOr`); S3 writes
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
alone, so a mixed Go/Python stack should set the selector explicitly. The scalar export skips
lists and nested maps a field holds whole (a route table, a model catalog);
`export_yaml_sections(loader, {field: dotted_key}, env_prefix=...)` exports each such section from
a `ConfigLoader` as one JSON `<PREFIX>_<FIELD>` default, which an env var already set still
overrides. `TelemetrySettings` reads the OTLP endpoint and trace sample rate under their standard
OTel names whatever the class's prefix, and `foundation/observability.start_observability`
configures logging and the trace export from it.

Business logic never reads the environment. In `go/`, env reads are confined to the config loader,
the `env` secrets backend, subprocess plumbing in `foundation/system`, `stdx.OrDefault`, and one
documented exception: the logger's deploy-time `GIT_SHA` (mirrored in Python).

### Static analysis (Go)

`.golangci.yml` runs with `linters.default: all`, `exclusions.generated: strict` and no
presets; the Go code carries no `//nolint` directive. Five linters are off: `exhaustruct` and
`wsl` (deprecated; `exhaustruct_v5` and `wsl_v5` replace them and stay on), `gomodguard`
(deprecated; `depguard` owns the import policy), `ireturn` (factories return interfaces by
design) and `nonamedreturns` (named results document multi-value contracts).

`exclusions.rules` holds the only exceptions:

| Linter | Scope | Why |
| --- | --- | --- |
| `spancheck` | `foundation/tracer/oteltracer/tracer.go` | The adapter hands a started span to its caller, who ends it. |
| `gosec` G204 | `clients/process/exec/exec.go`, `foundation/system/runner.go`, `tests/fixtures/reexec.go` | These run a program the caller names (or the test binary itself). |
| `gosec` G302 | `clients/toolchain/download.go` | An installed binary needs its executable bit. |
| `gocritic` hugeParam | `slog.Handler` implementations | The stdlib interface fixes `slog.Record` by value. |
| `lll` | a field line that is only a struct tag | Go cannot wrap a tag literal. |

Three rules are narrowed inside a linter that stays on:

| Linter | Setting | Why |
| --- | --- | --- |
| `revive` | `line-length-limit` disabled | `lll` owns the 90 columns and skips the single tokens Go cannot wrap. |
| `iface` | `enable: [identical, unexported]` | `unused` flags every `core/interfaces` contract, which other packages implement and consume; `opaque` flags a factory returning its interface, the reason `ireturn` is off. |
| `dupword` | `ignore: [end]` | An embedded Lua script closes a nested `if` inside a `for` with `end end`. |

Two settings scope a rule rather than exempt code: `tagliatelle` overrides keep the casing of
packages that mirror an external format, and `exhaustive` ignores enums this module does not
own (`connect.Code`, gRPC codes, `reflect.Kind`, Alloy tokens).

Test tables are built with `fixtures.Build` and `fixtures.BuildRef`, so `exhaustruct_v5` holds
in tests without spelling every zero field.

### Static analysis (Python)

`python/techai_webutils/pyproject.toml` configures every tool; `src` and `tests` carry no
`noqa`, `type: ignore`, `pyright: ignore` or `pragma: no cover`. ruff selects `ALL` with
preview rules; basedpyright runs in `all` mode over `src` and `tests`; vulture reads both trees
and deptry reads `src`. pydoclint reads both trees: every documented function's `Args:`,
`Returns:`, `Yields:` and `Raises:` sections match its signature and body, and every class
attribute carries an inline docstring. interrogate holds both trees at 100% docstring coverage,
nested closures included (1.7.0 counts them despite `ignore-nested-functions`). CI gates all of
them. Versions are pinned in the dev group.

ruff's `DOC` rules and pydoclint disagree on two shapes, so the code avoids both rather than
silencing either: re-raising a broad `Exception` (observe it with `foundation.errors.OnError`,
`AsyncOnError` or `OnFailure` instead) and raising a variable or a parameter's call (raise a
named class, a module function's result, or let a mock's `side_effect` fail in a test).

The only exceptions:

| Tool | Setting | Why |
| --- | --- | --- |
| ruff | `missing-trailing-comma` off | Conflicts with `ruff format`. |
| ruff | `missing-copyright-notice` off | The repo carries no per-file copyright headers. |
| ruff | `incorrect-blank-line-before-class` off | Mutually exclusive with D211. |
| ruff | `multi-line-summary-second-line` off | Mutually exclusive with D212. |
| ruff | `assert` off in `tests/**` | Pytest asserts are the test idiom. |
| basedpyright | `reportImplicitStringConcatenation` off | Implicit concatenation is how a long message wraps; ruff's ISC rules catch the accidental kind. |
| basedpyright | `tests` environment: the five `reportUnknown*` rules and `reportAttributeAccessIssue` off | `unittest.mock` doubles and pytest fixtures are untyped by design. |
| vulture | `ignore_decorators`: `@pytest.fixture`, `@overload`, `@override` | Called by injection, describe one implementation, or are called through a base. |
| vulture | `ignore_names`, grouped by reason in `pyproject.toml` | Names read by a framework, bound only for `cast("...")` strings, wire `TypedDict` keys read by subscript, or contract, config and API members consumers use. |
| deptry | `DEP002` for eight lazily imported packages | Loaded with `importlib.import_module` inside the backend that needs them, which deptry cannot see. |
| interrogate | `ignore-init-module`, `ignore-init-method`, `ignore-semiprivate`, `ignore-nested-functions`, `ignore-nested-classes` | The product's settings: the documented surface is the exported one. |
| interrogate | `ignore-overloaded-functions` | An `@overload` stub shares its implementation's docstring, and ruff's D418 forbids one on the stub. |

## Error codes

Every error crossing a package boundary carries an `ErrorCode`. The codes (`NOT_FOUND`,
`INVALID_INPUT`, `UNAUTHORIZED`, `FORBIDDEN`, `CONFLICT`, `TIMEOUT`, `UNAVAILABLE`, `UPSTREAM`,
`RESOURCE_EXHAUSTED`, `INTERNAL`, …) and every fact derived from one — its HTTP status, gRPC
status, retry class, client-safe message and whether an edge hides its message — have one source,
`taxonomy/error-codes.yaml`. `go generate ./go/core/apperr/...` (`go/tools/errcodes`) renders it
into Go `go/core/apperr/codes.go` (the constants, `Codes()` and the lookup behind `IsTransient`,
`ToHTTPStatus`, `GRPCStatus`, `SafeMessage` and `HidesMessage`) and Python
`core/errors/codes.py` (the `ErrorCode` enum and its maps and sets); the Go unit suite fails when
either committed output differs from a fresh render. `New(code, msg)` / `Wrap(err, code, msg)` build an
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
neither transient nor permanent, so no retry layer in either language retries it. Policies that
are not facts of a code stay hand-written beside their use: the retry policy, the principal
interceptor's status for an unresolvable caller, and the command-line exit codes.

## Stub-first backends

Most tiers ship an in-process, stub, or no-op backend, so wiring needs no cloud dependency:

- **Go:** analytics `stub`; lock `local`; outbox sink and outbox store `stub`; messaging, replay buffer, storage `memory`; secrets
  `env`/`file`; cache, llm, prometheus, transport `stub`; tracer `noop`; metrics no-op when disabled;
  logger `stdlib`. `database` and `rpc` default to `postgres` and `grpc`: their consumers hold the
  `*sql.DB` or connection itself.
- **Python:** cache `local`/`null`; email, safety `noop`; model_gateway `passthrough`; audit, dataset, embedding, facts, llm, retrieval,
  kb_ingestion, semantic_cache, token_ledger, vector `stub`; jobs, lock, messaging, storage `memory`; tracer, metrics `null`;
  executor `asyncio`.

Go cache (Redis), database (Postgres) and connector (S3, S3 Inventory) have no stub and are covered by
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
- `ops` and `runs`: the operations registry and its background-run table, which front-ends use to
  run named operations (see [Repository tooling substrate](#repository-tooling-substrate)).

Python `execution` mirrors these (`fan_out`, `run_job_group`, `new_gate`, `run_gate`) and adds the
`Executor` seam. `executor_from_config` chooses `AsyncioExecutor` (default) or `RayExecutor` over
the `RayRuntime` seam (the lazily imported `RealRayRuntime`, wrapped by the `ResilientRayRuntime`
retry decorator); `PooledExecutor` (a fixed pool of build-once workers) has no kind, so the caller
builds it directly. An unreachable Ray cluster is batch-wide, not per-item: the decorator's
single-flight connect raises `RayUnavailableError` once its retries are spent, `RayExecutor` connects
before the first item and raises it instead of failing every item, and `PooledExecutor` does the same
for the exception types passed as `abort_on`. A queue consumer therefore redrives the batch rather
than acknowledging a batch of failures. `RealRayRuntime` recovers a Ray Client session lost to a head
restart, and the Ray actor pool connects through the same runtime and restarts actors whose process
died. A Ray worker process never runs the consumer's entrypoint, so the consumer configures it through
`ExecutorConfig.ray_worker_setup_hook` (a dotted path Ray runs in each worker before its first task)
and `ray_log_encoding` (Ray's own task and actor log encoding). `ray_task_memory_bytes` reserves Ray's
`memory` resource for each task and pool actor, so Ray places memory-heavy work by memory as well as
CPU. `ray_task_num_cpus` declares each task's CPUs; when it is set, `RayExecutor.width` keeps at most
`floor(cluster CPUs / ray_task_num_cpus)` tasks of a batch in flight (never fewer than one, never more
than the caller's concurrency). It is unset by default, so a cluster that autoscales on pending tasks
still sees all of them. `ResilientRayRuntime` reports the connection's health and decides nothing:
`consecutive_connect_failures()`, the `ray_connect_failures_total{reason}` counter (the gRPC status
name), and `head_reachable()`, a TCP probe of `ray_head_probe_address` that separates a stuck client
from a down head.

Lanes size themselves from the process's resources. `core/interfaces/resource_probe` defines
`ResourceProbe.limits() -> ResourceLimits(cpu, memory_bytes)`, and `foundation/resources` builds
it. The `static` kind (the default) reports configured limits, unbounded unless set. The `cgroup`
kind reads cgroup v2 `cpu.max` and `memory.max`, falling back to the host for `max`, and with
`required` it raises `UNAVAILABLE` instead of falling back. `lane_concurrency(limits, per_item_bytes,
cap, reserve_bytes)` is `max(1, min(cap, floor(cpu), (memory - reserve) // per_item))`, so a lane on
the default probe runs at its cap. A parser declares its per-document cost through the
`CostedParser` capability (`ParseCost(memory_bytes, cpu)`); the isolated parser reports its
per-parse memory cap and one CPU. It waits on an executor the composition root sizes, and gets each
parsed document back through a scratch file rather than the result queue.

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

## Messaging capabilities

The messaging contracts are broker-neutral, and each Kind maps them onto its broker.

- **Keyed publishing.** `KeyedPublisher` (Go and Python) adds `PublishKeyed`, `PublishBatchKeyed`
  and `OrderedPerKey`. The key rides as the `partition_key` attribute (`PartitionKeyAttribute`).
  The memory Kind keeps per-key order; SQS standard queues carry the key but do not order by it.
  Redis Pub/Sub carries no attributes and does not implement it. `Decorate` keeps the capability.
- **Delivery settings.** Python `DeliveryConfig` (`max_deliveries`, `retry_backoff`,
  `dead_letter_topic`, `max_processing_seconds`) is unset by default.
  - SQS sets a failed message's visibility from its receive count.
  - SQS checks the queue's redrive policy and visibility timeout with one `GetQueueAttributes` per
    subscribed topic, bounded by `SQSConfig.attributes_timeout_seconds`, and only when a setting
    needs it.
  - Memory redelivers, then moves the message to the dead-letter topic.
  - `LeaseExtender.extend` keeps a long handler's message leased; on SQS it is
    `ChangeMessageVisibility`.
  - `SQSConfig.topic_queue_urls` maps topics to queues.
  - `MessagingConfig.max_payload_bytes` (256 KiB) rejects a larger payload with `INVALID_INPUT`
    before it is sent.
- **Backlog.** `BacklogProbe.backlog(topic)` returns `Backlog(messages, oldest_age_seconds)`, and
  `BacklogGauges` publishes it as `messaging_backlog_messages{topic}` and
  `messaging_oldest_age_seconds{topic}`, the gauges alerts and autoscaling read. SQS reports
  `ApproximateNumberOfMessages`. It has no age attribute (the age is a CloudWatch metric only), so
  its age is `None` and that gauge is left unset.
- **Conformance.** Python's `tests/contract/messaging_conformance.py` is the contract every Kind
  passes: memory in the unit suite, SQS against ElasticMQ in the integration suite.

### Moving to Kafka

A `kafka` Kind is added beside `sqs` and `memory` and selected by config; no caller changes. It
must:

- pass the transport conformance suite;
- implement `KeyedPublisher`, with `OrderedPerKey` true, since a key maps to a partition;
- commit offsets contiguously while handlers run concurrently: an offset is committed only once
  every earlier offset in its partition has settled;
- implement the retry backoff with retry topics, and `dead_letter_topic` as a DLQ topic;
- report consumer-group lag through `BacklogProbe`;
- implement `LeaseExtender` by keeping the poll loop inside `max.poll.interval.ms`;
- run an idempotent producer (`enable.idempotence`), so a producer retry is not a duplicate record.

The outbox relay still delivers at least once: a send that succeeds before its row is marked sent
is sent again, and consumers absorb that through stage idempotency. A product's outbox table adds
two columns for partitioned relays:

```sql
partition_key text not null default ''
partition     int  not null default 0
```

The writer sets `partition = fnv32a(partition_key) mod partitions` at insert, using
`types.OutboxPartition`, and the store never recomputes it. The engine store reads the key from
`KeyColumn`, so a table may point `KeyColumn` at `partition_key` or keep its existing key column.
Lowering `partitions` strands rows in the dropped partitions until a migration rewrites them.

## Resumable ingestion

An ingestion pipeline records each finished stage, so a retry or a re-index resumes where it
stopped instead of paying for every stage again. Python only; the defaults change nothing.

- **Stages and fingerprints.** `Stage` is `parse`, `enrich`, `chunk`, `embed`, `index`.
  `foundation.checkpoint.fingerprint(stage, versions, upstream)` is the SHA-256 of the stage, the
  versions that shape its output (parser, model, chunker settings) and the upstream stage's
  fingerprint, so a version change upstream invalidates every stage after it.
- **Checkpoint store.** `CheckpointStore` (`get`, `put`, `put_part`, `get_part`, `parts`,
  `delete`, `purge`) keeps each stage's artifact under `CheckpointKey(org_id, document_id,
  content_hash, stage, fingerprint)`. `CheckpointConfig.kind` selects it: `noop` (the default)
  stores nothing, so every stage runs as today; `s3db` writes artifacts to the object store and
  records each stage through the injected `StageStateClient`.
  - Artifacts are keyed by content within an org, so two documents with the same bytes share them;
    a holder marker per document means a delete removes an artifact only when no document still
    holds it.
  - A payload over `max_artifact_bytes` (64 MiB) is written as segments, so no object outgrows the
    cap.
  - A corrupt envelope reads as a miss, so the stage runs again; one written by a newer release
    is refused with `CONFLICT`.
- **Resume planner.** `ResumePlanner.plan(states, current, *, sink)` returns the first stage with
  no record carrying today's fingerprint, or `None` when the document is indexed. For a sink that
  embeds by itself (`embedding_sinks`, Bedrock by default) `embed` is skipped. The golden cases in
  `testdata/resume_planner/` are the contract a port of the planner replays.
- **In-flight claims.** `InflightClaim` lets one of several concurrent consumers run a costly stage
  of one content. `inflight_key(org_id, content_hash, stage, fingerprint)` names the work.
  `InflightClaimConfig.kind` selects it: `noop` (the default) grants every claim; `redis` is
  `SET NX PX` on a shared key that lapses with the message lease, and its release deletes the key
  only while it still holds the caller's token. A Redis error grants the claim, since a duplicate
  run is today's behaviour and the stages are idempotent.

## Bedrock ingestion limits

- **Start budget.** `StartBudget.acquire(binding) -> Slot | None` and `release(slot)` cap the
  Bedrock ingestion jobs an account runs at once and pace their starts. A caller holds its slot
  for the life of the job and releases it when the job is terminal; a caller that reattaches to a
  running job acquires again. `StartBudgetConfig.kind` selects it: `noop` (the default) grants
  every acquire at once; `postgres` takes slot `i` as the advisory lock `<account>:<i>` (the
  injected `new_lock`, the postgres `clients.lock` Kind) and paces starts through one row per
  account, so every pod shares `slots` (5) and `min_start_interval_seconds` (10 s). A waiter gets
  `None` after `acquire_timeout_seconds`. The product owns the table:

  ```sql
  create table bedrock_start_rate (
      account       text primary key,
      last_start_at timestamptz not null
  );
  ```

- **Bedrock start and cohort.** `start_ingestion_job(..., round_id=…)` sends a `clientToken`
  derived from the knowledge base, data source and round, so a retried start is idempotent.
  `ServiceQuotaExceededException` and `ThrottlingException` are `UNAVAILABLE` (transient);
  `ConflictException` stays `CONFLICT`. `iter_cohort(..., s3_uris=…)` yields a round's documents:
  Bedrock looks them up with `GetKnowledgeBaseDocuments`, ten per call and five calls per second;
  any other Kind filters `list_documents`.

## OpenSearch store and sinks

Python only; the defaults (`qdrant` store, `noop` sink, `stub` metrics querier) change nothing.

- **`opensearch` VectorStore Kind.** k-NN (`knn_vector`, faiss HNSW, cosine) and BM25 (`text`) in
  one index per collection. The composition root builds the `AsyncOpenSearch` client (hosts,
  SigV4 auth) and injects it through `new_vector_store_from_config(..., opensearch_clients=…)`;
  every request carries `OpenSearchConfig.request_timeout_seconds`.
  - **Tenancy.** `layout: shared` puts a collection's orgs in one index, routed by org;
    `dedicated` gives each org its own alias and index. The store adds the org filter to every
    search, count, lookup, update and delete. The org comes from a scoped view (`for_org`), the
    search filters, or each entry's `org_id`; a call without one is `INVALID_INPUT`. A document's
    `_id` is `<org>/<entry id>`, so two orgs' equal ids never collide.
  - **Versioned indices.** Reads go through an alias pointing at `<collection>-<model>-v<version>`,
    stamped with its model in `_meta` (`index_model`). A re-index writes to both indices
    (`set_write_targets`, per process), moves the alias atomically (`swap_alias`, the old index
    kept) and can swap back.
  - **Vector shape.** `vector_mode: on_disk`, or `create_index(expected_docs=…)` above
    `disk_mode_threshold_docs`, maps the vector `on_disk` at `disk_compression_level` (default
    `16x`). `oversample_factor` sets the k-NN query's rescoring; rescoring is never off, since a
    binary-quantized index loses about 0.4 recall@10 without it. `truncated_dimension`
    (Matryoshka) searches the vector's first N components and rescores the candidates by a
    Painless cosine over the full vector, kept in `_source` only (not a second `knn_vector`,
    whose graph OpenSearch 2.19 builds even with `index: false`).
  - **Bulk ingest.** `begin_bulk_ingest(index)` records refresh and replicas in `_meta` and turns
    them off; `end_bulk_ingest(index)` restores and verifies them, and is safe after a failed
    backfill or a second call. `bulk.bulk_ingest(store, index)` pairs them around one backfill,
    on a single- or multi-domain store.
  - **Several domains.** With more than one injected client, `MultiDomainVectorStore` routes each
    entry by its `shard` metadata through `shard_domains`, or, without one, by a stable hash of
    its document id; reads, counts and deletes fan out, and a search merges every domain's hits
    by score into the global top k (shards of one index share a scale; fusion runs later, across
    retrievers). With `OpenSearchConfig.domain_deadline_seconds` set, a domain that has not
    answered by then is left out and counted on `vector_domain_search_cut_total{domain}` (the
    builder's `metrics`). An unmapped shard is `INVALID_INPUT`.
  - **Administration.** `snapshots.SnapshotAdmin` registers an S3 or fs repository, ensures a
    Snapshot Management schedule and restores an index under a new name. `tiers.TierAdmin`
    ensures an ISM policy (warm, cold, delete by index age) and attaches it to the indices a
    caller retires, never by name pattern, since a warm index is read-only.
    `index_admin.count_org_documents(clients, index, org)` counts an org's distinct documents in
    an index across every domain (a paged composite aggregation, so it is exact), and
    `index_admin.delete_unaliased_index(clients, index)` deletes an index on every domain only
    when no alias on any domain names it: the checks a per-org reindex runs before its swap and
    when it prunes the previous index. `MultiDomainVectorStore.create_index` and `swap_alias`
    create the shadow and swap the org's alias on every domain.
- **Sink.** `Sink.write(document, chunks, vectors)`, `delete(org_id, document_id)` and
  `update_metadata(org_id, document_id, fields)` end the ingestion pipeline. `SinkKind.opensearch`
  writes through the injected store: stable ids `<document>:<n>`, the document's metadata,
  `content_hash`, the embedding model, the chunk's structure fields and `entities.<label>` lists.
  Vectors from another model than the index's are a permanent `CONFLICT`.
  `SinkKind.bedrock` takes no vectors (Bedrock embeds by itself) and writes files plus
  `.metadata.json` sidecars (`{"metadataAttributes": …}`) into `SinkConfig.bedrock.bucket`
  through the injected `StorageClient`, under `{prefix}/{org}/{hash2}/{document}/{version}/`
  (`hash2` from the document id, `version` the content hash's first 16 characters).
  `layout: chunk_files` (the default) writes one `{n}.txt` per chunk for a `NONE`-chunking data
  source; `kb_units` packs contiguous chunks up to `max_unit_bytes` (25 MiB) for `FIXED_SIZE`.
  A write lists the document once, skips present keys (a redelivery writes nothing) and deletes
  other versions; `delete` removes every object; `update_metadata` rewrites only the sidecars.
  `object_tags` (empty by default) tags every object written, so a bucket lifecycle rule can
  select the sink's files wherever their prefix lies. The caller then starts the data source's
  ingestion job.
- **`MetricsQuerier`** mirrors Go's: `query(promql)` and `query_range(promql, start, end, step)`.
  Kinds `stub` (zero, no I/O) and `http` (the Prometheus query API, fail closed, coded errors).
- **`VectorKnowledgeBase(embed_concurrency=…)`** keeps that many sub-batches in flight (1, the
  default, is one at a time).
- **Hybrid retrieval.** A store with keyword search (`VectorStore.supports_keyword`; OpenSearch)
  gets the k-NN and the BM25 search concurrently, fused by `RetrievalConfig.fusion`: `rrf` (the
  default, `Σ 1/(k + rank)`, `k` 60) or `convex` (min-max or z-score normalized, weighted by
  `dense_weight`). Each result's `score` is then the fused score. Shards of one index are merged by
  score (`merge_by_score`), never fused. The `opensearch` `RetrievalKind` builds this engine over
  the injected store; Qdrant and the stub keep pure k-NN.
- **Abstention and passage order.** `RetrievalConfig.abstainer` `score_threshold` (the default)
  drops passages below `min_score` (0.5), reading whatever score the engine returns (a reranker's
  when reranking is on); `top_score` (`TopScoreAbstainer`) keeps the whole pool when its best
  passage reaches `abstain_threshold` and returns nothing otherwise, so a calibrated bar works on
  any scale and a weak pool skips the LLM call; `none` keeps every candidate, as fused RRF scores
  (about 0.03) need.
  `passage_order: edges` puts the best passages at both ends of the prompt (ranks 1, 3, 5 … 4, 2);
  the reordering is in the engine's output, so `[n]` and the citations stay aligned.
  `passage_header(n, passage)` gives the consumer's prompt `[n] <document> · <section> · p. <page>`.
- **Duplicate passages.** With `RetrievalConfig.collapse_duplicates`, passages whose text is equal up
  to case and whitespace, or that share a `collapse_cluster_key` value (a near-duplicate cluster id),
  collapse into the best-scored one, whose `also_in` metadata lists the others' document ids
  (`clients.retrieval.collapse`). It runs after the scope policies, so an out-of-scope copy is never
  named, and before abstention and ordering. Off by default.
- **Off-Bedrock model Kinds.** `EmbeddingKind.openai_compatible` and `LlmKind.openai_compatible`
  speak `/v1/embeddings` and `/v1/chat/completions` (vLLM, TEI, LiteLLM, llama.cpp, OpenAI) with
  an optional bearer `api_key` (never in a repr). A stream that sends nothing for
  `stall_timeout_seconds` (60) fails with `TIMEOUT`. HTTP failures map through
  `foundation.resilience.http_boundary.httpx_error_to_app_error` (429/5xx transient).
- **Query and document embeddings.** `EmbeddingProvider.embed` / `embed_batch` take
  `input_type` (`query` or `document`, the default). Query-time callers (the vector engine, the
  semantic cache, the route classifier) pass `query`. `EmbeddingConfig.query_prefix` /
  `document_prefix` (empty by default, so vectors are unchanged) wrap the provider in
  `PrefixedEmbedding` for an asymmetric model (nomic `"search_query: "` / `"search_document: "`, E5
  `"query: "` / `"passage: "`); setting one needs a re-embedded corpus.
- **Reranking.** `Reranker.rerank(query, passages) -> scores` (`clients.reranking`: `noop`, the
  default, and `cross_encoder` over an injected `RerankClient`, in `batch_size` batches).
  `RetrievalConfig.reranking_kind` `cross_encoder` wraps any engine in `RerankingRetrievalEngine`
  before the abstainer; `bedrock_rerank` stays Bedrock Retrieve's own and is refused on any other
  kind, as is an unknown value.
- **Neutral filters and citations.** `FilterExpr` (`FilterEq`, `FilterIn`, `FilterAnd`;
  `equals_all(map)`) translates to Bedrock (`bedrock_filter`, `expr_filter_builder` for the
  engine's `filter_builder`) and to OpenSearch (`filter_clauses`). `Citation` gains `chunk_id`
  (the passage's `chunk_id` metadata, which vector passages now carry) and `span` (`None`: the
  whole chunk).
- **Verified citations.** `PassageCitationExtractor(verify=)` is `off` by default: one citation
  per passage, in order, with the retrieval score as confidence, as before. `lexical` and `nli`
  parse the answer's `[n]` / `[1, 3]` markers and return only the cited passages, in marker
  order, each with its `marker`. Each is scored against the answer sentences that cite it (a
  marker alone on a line cites the sentence before it) by a `CitationScorer`
  (`core.interfaces.citation_scorer`): `lexical` is `LexicalCitationScorer`, the share of the
  claim's content words in the passage, on CPU with no model call; `nli` is the consumer's
  entailment model, injected as `scorer`. The best score is the `confidence`, never the
  retrieval score. Under `min_confidence` (0.5) a citation is kept with `supported=False`
  (`unsupported: flag`, the default) or dropped (`strip`); `keep_markers(answer, keep)` then
  removes the dropped markers from the answer.

## Connector sources

`interfaces.ConnectorSource` is the read seam a sync crawls a tenant's source with:
`TestConnection` and the resumable, page-at-a-time `ListPage(token, limit)`. The Go
`clients/connector` builder picks the backend by `SourceConfig.Kind`:

- **`s3`** (the default an S3 connector keeps) lists the bucket under `Prefix`
  (`ListObjectsV2`; the token is S3's continuation token).
- **`s3_inventory`** reads the same objects from the bucket's S3 Inventory report:
  `InventoryManifestKey` (in `InventoryBucket`, default `Bucket`), then the gzipped CSV data
  files it lists. With no manifest key, `InventoryPrefix` names the report's configuration
  folder and the source reads the newest dated report there whose `manifest.checksum` is
  written (none yet is `NOT_FOUND`); its tokens then start with that report's folder
  (`<stamp>/<file>:<row>`), so a resumed crawl finishes the report it began. A crawl of hundreds of millions of keys costs one GET per data file instead
  of one LIST per 1,000 keys; the snapshot is as fresh as the latest report. Objects match the
  `s3` kind's (key URL-decoded, ETag quoted, RFC 3339 time); rows of another bucket, outside
  `Prefix`, non-current versions and delete markers are skipped. The token is
  `<data file index>:<rows consumed>`; the source keeps the current data file open between
  pages and re-downloads it once if that body has died. Only CSV reports are read (ORC and
  Parquet are `INVALID_INPUT`), and the report must list `Bucket` and carry the `Bucket`,
  `Key`, `ETag` and `Size` columns (without ETags every crawl reads as changed; without
  sizes the oversized-object cap never fires).

Both kinds authenticate through `connector/s3.NewStorage` (access key, or STS AssumeRole with
the tenant's ExternalID) and classify object-store failures with `ClassifyListError`
(configuration → `INVALID_INPUT`, transport and throttling → `UNAVAILABLE`).

## Entity annotation

`core.interfaces.annotator` is the NLP seam the enrich stage tags chunks through:
`Annotator.annotate_batch(blocks, tasks, profile) -> list[Annotation]`, one `Annotation` per
`TextBlock`. An `Annotation` carries `entities` (`Entity(label, start, end, text, confidence)`,
character offsets into the block), `task_results` keyed by task name, the `model` and `version`
that produced it, and `degraded`.

- **Tasks.** `TaskRegistry` maps a task name to its `TaskResult` type. It knows `entities` out of
  the box; a consumer registers its own (`register("speaker", Speaker)`) at its composition root,
  with no engine change. An unregistered task is `INVALID_INPUT`; registering a name under another
  type is `CONFLICT`. There is no global registry.
- **Kinds** (`clients.nlp`, `AnnotatorKind`, selected by `AnnotatorConfig.kind`):
  - `noop` (the default) returns empty annotations, so the graph boots with no model;
  - `spacy` loads the `model` pipeline lazily, in a thread, with only its `ner` component enabled
    (the `nlp` extra installs spaCy);
  - `gliner` calls an injected `GlinerModel` with `labels` and `threshold` (the engine carries no
    GLiNER or torch dependency);
  - `grpc` calls the consumer's NLP service through an injected `AnnotateClient`; it is the only
    kind that runs consumer tasks, and the `profile` selects the service's task set.

  `labels` filters the `spacy` kind's entities. `check_annotations` checks every backend's reply
  against the blocks (one annotation per block, spans inside the text and matching it, each task
  result of its registered type) and raises `INTERNAL` on a mismatch.
- **Unavailable backend.** `AnnotatorConfig.on_unavailable` is `wait` by default: a transient
  error (`UNAVAILABLE`, `TIMEOUT`) reaches the caller, which retries. `degrade` wraps the annotator
  in `DegradingAnnotator`, which returns `Annotation(degraded=True)` per block and counts
  `nlp_degraded_total{task}`; a permanent error is always re-raised.
- **Entity ruler.** `EntityRulerAnnotator(inner, patterns)` applies an org's `RulerPattern(label,
  phrase)`s after `inner`: each phrase matches as whole words, case-insensitively, the longest
  first, and a statistical entity overlapping a ruler span is dropped. The patterns are the
  caller's, built per org; with none, or without the `entities` task, `inner`'s output is
  returned unchanged.
- **`pii` task.** `PiiAnnotator(inner, registry=)` answers `pii` in process (register
  `PII` -> `PiiSpan`) and passes the other tasks to `inner`, which is not called when `pii` is the
  only task. `find_pii` finds formats, not meaning: e-mail addresses, phone numbers (10 to 15
  digits written with `+` or separators), US SSNs, Luhn-valid card numbers and IPv4 addresses;
  names and addresses need a model's entities. `apply_pii(text, spans, mode)` redacts
  (`[EMAIL]`), masks (`*` per character, offsets kept) or keeps the text.

## Sentences and language, in process

Sentence boundaries and block languages are needed for every block, so both run in the stage
that needs them, never through the NLP service.

- **`SentenceSplitter.split(text, *, language=None) -> list[TextSpan]`**
  (`core.interfaces.sentence_splitter`). The spans tile the text: the first starts at 0, each
  starts where the previous ends, the last ends at `len(text)`; empty text has none.
  `clients.sentences` has one kind, `sentencizer` (the default): spaCy's rule-based
  `sentencizer` on a blank pipeline per language (the `nlp` extra; no model, no download). The
  tokenizer's exceptions keep abbreviations and decimals whole; on one core it splits about
  270,000 words/s. `SentenceSplitterConfig.default_language` (`en`) applies when `split` names
  no language. A language without spaCy rules, or whose tokenizer needs a library that is not
  installed (`ja`, `ko`, `th`, `vi`), uses the multi-language `xx` rules, counted in
  `sentence_splitter_fallback_total{language}`. A text over `max_chars` (1,000,000) is
  `INVALID_INPUT`. `SentenceSplitterConfig.join_patterns` (empty by default) wraps the splitter
  in `RuledSplitter`, which joins a span whose stripped text fully matches a pattern onto the
  next one. `TRANSCRIPT_JOIN_PATTERNS` keeps a transcript's `Q.` / `A.` markers, speaker labels
  (`THE WITNESS:`) and trailing honorifics (`MR.`) with what follows them.
- **`LanguageIdentifier.identify(text) -> LanguageGuess(code, confidence, script)`**
  (`core.interfaces.language_identifier`). Blank or unidentifiable text is `UNDETERMINED_GUESS`
  (`und`, 0). `clients.language` kinds:
  - `langdetect` (the default) is today's backend: the library and seed the document metadata's
    language uses (ISO 639-1 codes);
  - `fasttext` runs a `lid.176` model file (ISO 639-1); `glotlid` a GlotLID file (ISO 639-3 code
    and ISO 15924 script). Both read `model_path` on the first call through `fasttext-predict`
    (the `fasttext` extra), with the text's whitespace collapsed to one line. On the committed
    parity set `lid.176` agrees with `langdetect` and is about 100 times faster; it becomes a
    consumer's default only after its own parity and baseline check;
  - `lingua` runs Lingua (the `lingua` extra, a ~170 MB wheel CI's all-extras sync leaves out),
    which stays accurate on a few words.

  Two decorators apply from the config: `short_text_kind` with `short_text_chars` routes shorter
  text to its own backend (`ShortTextRouter`, e.g. `lingua` for queries and interview answers), and
  `min_confidence` reports `und`, at the guess's confidence, for a guess below it
  (`ConfidenceFloor`, outermost).
- **`NearDuplicateDetector.check(scope, document_id, text, *, ref) -> NearDuplicate | None`**
  (`core.interfaces.near_duplicate`). It places a document in the cluster of the most similar
  earlier document of its scope above the threshold, or a new cluster it represents, and records
  it; a second check of the same document returns the same cluster. `NearDuplicate` carries the
  cluster id (the representative's document id), the representative's `ref` (an opaque caller
  value, e.g. its content hash), the similarity and the document's signature.
  `clients.near_duplicate` kinds: `noop` (the default) detects nothing; `minhash` computes a
  MinHash signature (`num_perm` hash functions over the text's lowercased word `shingle_size`-grams,
  at most `max_shingles` of them, the smallest hashes, so near-copies keep the same sample) and
  looks candidates up in LSH band buckets kept in the injected `Cache` with `ttl_seconds`: one read
  per band plus one per candidate, never a scan. `lsh_partition` picks the band split whose curve
  midpoint is the highest not above `threshold`; each candidate is verified on the whole signature
  (the fraction of agreeing positions, the Jaccard estimate). The cache has no compare-and-set, so
  two concurrent near-copies may both start a cluster.
- **`TokenCounter.count(text) -> int`** (`core.interfaces.token_counter`): a text's tokens as an
  embedding model counts them, in process and subadditive, so the sentence chunker's `tokenizer`
  can be `counter.count`. `clients.tokenizer` kinds: `approx` (the default, no model file)
  counts `ceil(len(text) / chars_per_token)`, 4 by default (the `fixed` chunker's 2,048 characters
  to 512 tokens); `huggingface` (the `tokenizers` extra) loads the model's `tokenizer.json` from
  `path` and counts its ids without special tokens. The deployable ships the file.
- **`DistinctCounter.observe(keys, member) -> list[int]`** (`core.interfaces.distinct_counter`):
  records `member` under each key and returns each key's distinct count, so a redelivery counts
  once (e.g. the documents of an org that carried a block of text). The counts are advisory: a
  backend that cannot reach its store returns zeros and never raises. `clients.distinct_counter`
  kinds: `noop` (the default) counts nothing; `redis` keeps a HyperLogLog per key (`PFADD` +
  `EXPIRE ttl_seconds` + `PFCOUNT` in one pipeline; about 0.8% error, at most 12 KB a key), on the
  injected client or one it opens, counting absorbed errors in `distinct_counter_errors_total{op}`.

## Sentence chunks and chunk context

`ChunkerKind.sentence` (`clients.chunking.sentence`) builds a `SentenceChunker`, which satisfies
both `Chunker` (`chunk(text) -> list[str]`) and `StructuredChunker`
(`chunk_spans(text, *, entities=(), turns=()) -> list[StructuredChunk]`). `fixed` stays the
default.

- **Blocks.** `split_blocks` reads the parser's markdown: ATX headings set `section_path`; fenced
  code, pipe tables (a `|` row and a separator row) and paragraphs are blocks; a table's caption
  is the one-line paragraph before it. Blocks tile the text.
- **Packing.** Consecutive paragraphs under one section, each table and each code block form a
  run; a heading opens the run after it. Units are sentences (from the injected
  `SentenceSplitter`, at most `window_chars` per call), table rows after one header unit, or code
  lines. Each unit is counted once by the injected tokenizer (the embedding model's own) and
  packed greedily up to `max_tokens`, so chunking is linear in the text; a unit over the budget
  is cut at whitespace, else mid-word. Paragraph chunks repeat up to `overlap_tokens` of the
  previous chunk's whole sentences (0 by default). A table piece after the first repeats the
  header rows, unless they are over half the budget.
- **`StructuredChunk`** carries the text to embed, the source span `[start, end)` (with overlaps
  removed, the spans tile the text), `section_path`, `element_type` (`paragraph`, `table`,
  `code`, `heading`), a table's `caption`, the input entities wholly inside its span, and the
  input transcript `turns` its span overlaps (a long turn is on every chunk it spans).
- **Context.** `ChunkContextualizer.contextualize(document, chunks) -> list[str]` returns the text
  to embed and index per chunk (`clients.chunking.context`): `none` (the default) unchanged;
  `header` prepends `"{document} > {section} > {subsection}"` and a table's caption,
  deterministically; `llm` prepends one or two model-written sentences, one call per chunk at
  temperature 0 (bounded by `max_document_chars`, `max_context_tokens` and `concurrency`), a
  priced dial a consumer enables only with a budget.

## Structured parse and the layout parser

`core.domain.document_ir` is the parse IR: `DocumentBlock`s in reading order, typed by `BlockType`
(`title`, `heading` with its `level`, `paragraph`, `list_item`, `table`, `image`, `code`,
`caption`, `formula`, after Docling's document model, and `turn`), each with its page,
`section_path` and `order`. A `turn` block carries its `Turn`: the speaker's role and id, its index,
its span in the text, the interview question it belongs to, whether it is a follow-up, and its
recording times when the source has them. A table keeps its `TableCell`s (row, column, header flag, spans), size, markdown
rendering and caption. `ParsedDocument.blocks` holds them (empty unless a layout-aware parser
ran; `markdown_content` is unchanged). `blocks_to_json` / `blocks_from_json` give the JSON form
under `FORMAT_VERSION`, read at N and N-1; `parsed_to_json` / `parsed_from_json` do the same for a
whole `ParsedDocument` under `PARSED_FORMAT_VERSION` (the parse stage's checkpoint artifact).
`ChunkPayload` gains `section_path`, `element_type` and `span`, all optional.

`ParsedDocument.source_path` (empty by default, kept by the JSON form) names the file, so a
splitter can choose by extension. `splitter_from_config` (`clients.splitting`) builds a
`DocumentSplitter` by `SplitterKind`: `kb_unit` (the default) packs paragraphs up to `max_bytes`;
`markdown` cuts at `##` headings outside code fences, prefixes each section with `# <title>`
(frontmatter `title`, else the first `#` heading), and strips a YAML frontmatter mapping from
the text into every unit's `ChunkPayload.frontmatter`; `code` (the `code` extra: tree-sitter with
the Go, Python and TypeScript grammars; without it the kind is `INVALID_INPUT`) makes one unit per
top-level declaration with the comments directly above it, a Go method carrying its receiver
type's declaration line, and splits a declaration over `symbol_max_bytes` (8 KiB) at its body
into `Parent.child` units; a file with no grammar falls back to `kb_unit`. Their units carry
`symbol` and the 1-based `line_start` / `line_end`, and tile the file's lines.

`ParserKind.layout` (`clients.parsing.layout`, the `layout` extra) converts PDFs with Docling
inside the same bounded child as `isolated` (`memory_bytes`, `timeout_seconds`); other formats go
through `isolated` unchanged. `parse_pages(path, first_page=, last_page=)` converts one page range,
so a large PDF can parse in checkpointed parts; each part is one bounded child, so it costs
`cost()`. `page_count(path)` opens the PDF with pymupdf in the same bounded child (0 for a non-PDF
or unreadable file, meaning parse it whole), so the parser is a `PagedParser`
(`core.interfaces.parser`), and `join_parsed(parts)` (`core.domain`) joins the parts in page order,
renumbering block `order`; a failed part fails the joined document. The child is spawned, not forked (Docling's torch stack aborts in a forked child on
macOS; the forkserver does not preload Docling, so a fresh interpreter costs about the same).
`artifacts_path` points Docling at model weights baked into the image (empty: Docling downloads
them on first use); `ocr` runs OCR on pages and regions with no text layer. Furniture (page
headers and footers) is dropped, and a table's or picture's caption travels with it. Measured on
a three-page PDF (two columns, a ruled table, an image-only page): reading order kept, the table
as one 4x2 block with its header, the scanned page OCR'd, and a peak child RSS of about 1.7 GiB,
so `layout` needs a `memory_bytes` of several GiB. `parser_from_config` refuses `layout` when
Docling is not installed unless a `convert` function is injected.

A child that raises `MemoryError` under its address-space cap now fails its document with
`parse_isolation_failed: memory_exceeded` (`MEMORY_EXCEEDED`, raised as `IsolationMemoryError`),
for both kinds.

## Repository tooling substrate

Go packages for tools that read and check a codebase (linters, guards, generators, developer
CLIs), so each one does not re-walk the tree or re-implement a runner.

- **Codebase.** `interfaces.Codebase` is one read of a working tree: `Files(FileSelector)`,
  `Text`, `Match`, `Changed`, and typed views (`Go()` parses Go files, `YAML()` queries and decodes
  YAML documents, `Rendered` renders Helm charts). `foundation/codebase` builds it over a
  `CodebaseSource` with a `Snapshot` that memoizes every read and parse for the run, so N checks
  over one tree read each file once. `clients/codebase` supplies the source by `Kind`: `git`
  (tracked plus untracked, ignoring what `.gitignore` excludes), `fs` (a directory, through
  `os.OpenRoot`), `memory` (a map, for tests). `foundation/yamlq` queries and edits YAML in place.
- **Knowledge bundles.** `foundation/okf` reads and writes Open Knowledge Format v0.2 markdown:
  `Parse` splits a file into typed frontmatter, body and classified links; `RenderFrontmatter`,
  `RenderIndex` and `RenderRootIndex` write frontmatter and `index.md` deterministically; `Conform`
  and `ConformLog` report a file that breaks the format or a caller's per-type required keys. It is
  pure (no I/O): a guard or generator walks the `Codebase` and passes each file's bytes.
- **Path theories.** `foundation/paththeory` compiles an ordered rule set (a kind and a whole-path
  template with `**` and `{token}`s: the built-in name styles and number shapes plus declared
  patterns or closed lists) and claims each path by the first matching rule. `Classify` returns a
  tree's claims, its unclaimed paths and its dead rules, so a guard can require every file to have
  exactly one place and name; a `forbid` rule names a violation instead of claiming it. A
  catch-all template is refused. It is pure (no I/O).
- **Probes.** `interfaces.Probe[T]` is one check returning a `types.Verdict` (status, findings,
  transient). `services/probe.NewSet` runs a set in parallel with a per-probe timeout, retries of
  transient failures and a strict mode that fails a skip, returning one step per probe in probe
  order.
- **Tool runs.** `types.ToolRun` is a data row that drives an external binary: its targets
  (`services/toolrun/selectors`: files, Go modules, staged files, …), one invocation per target
  or one over all of them, fix arguments, and the parser that turns its output into a verdict
  (`services/toolrun/parsers`). `foundation/stepcache` skips a step whose inputs a previous pass
  already covered. The key covers the inputs, the tool's version and the row's whole definition;
  a caller salts the cache with the checker's build and the configuration it reads
  (`Config.SaltBinary`, `SaltDirs`), derived on first use, so a rebuilt or reconfigured checker
  misses too.
- **Reports.** `foundation/report` renders a gate's steps as lines, tables, GitHub Actions
  annotations or JSON (`report.Composite` fans out). The line reporter prints a status line
  per phase, a failure block (each failed step's actionable output lines and a configured
  remediation hint) and a summary; `Interactive` adds a tick per step and `Color` ANSI
  styling. `report.Console` is a CLI's whole console on one stream: verbosity levels, a
  leveled `Logger`, a command hook, coded-error formatting with exit codes
  (`report.ExitCode`), the GitHub step summary, and `Observer()`, which picks the table
  when verbose, the line reporter with annotations in CI, and the interactive line
  otherwise. The composition root resolves TTY, color and CI into `ConsoleConfig`; the
  console reads no environment.
- **Generators.** `interfaces.Generator` derives committed files from a codebase;
  `foundation/gen.Check` diffs a fresh render with what is committed and `Apply` writes it;
  `foundation/scaffold` plans and applies templated files with a preserve marker;
  `foundation/impact` maps changed paths to what they affect. Built-in generators:
  `foundation/pins` (a Renovate config from the detected ecosystems and a consumer `Policy`) and
  `foundation/schemagen` (JSON Schema reflected from Go types, Kind values extracted from factory
  source, and a config reference). `foundation/dataset` merge-loads a directory of YAML data
  files into strictly decoded sections, and holds the merged data to its constraints as
  data: `Check` evaluates CEL rule rows over the sections, `Validate` a JSON Schema (such as
  one `schemagen` reflects from the sections' types).
- **Clients.** Typed command-line clients behind interfaces, each `stub|exec`: `clients/git`,
  `helm` (plus `helm.NewRenderer` for `Codebase.Rendered`), `kubectl`, `docker`, `terraform`;
  `clients/process` supervises long-lived child processes (process groups, graceful stop).
  `clients/aws/<family>` (`sts`, `secretsmanager`, `ses`, `bedrock`, `bedrockagent`,
  `servicequotas`, `iam`, `route53`) wrap one SDK seam each, `stub|sdk`, through `client.Call`; `clients/session`
  resolves and caches verified credentials. `clients/toolchain` declares developer tools as data
  (`types.ToolEntry`), detects them, installs them per platform (pinned downloads verified by SHA-256),
  and checks that each go-installed CI tool on PATH is the pinned build.
- **Operations.** `execution/ops` registers typed operations once (descriptor data plus a
  `Builder`; `ops.Func` reflects the input schema from the input type) and admits them per
  front-end by an `ExposurePolicy` (safety-class ceiling, lifecycles, allow and deny lists); it
  runs one at a time, each under recovery → timeout → logging → code mapping.
  `execution/runs` tracks background runs of long-running operations. Front-ends:
  `transport/cobra` (a command whose flags come from the input's `flag` and `arg` tags),
  `transport/mcp` (one MCP tool per admitted operation plus `runs.status|result|cancel|list`;
  a result carries each step's status, a failed step's log tail, and the detail of every step
  built with `types.OutputStep` under `output`, so a passing operation returns its answer)
  and `transport/cron` (a scheduled job runs one named operation unattended).

Generic methods on concrete types (Go 1.27) carry typed results without a type switch:
`Snapshot.Memo[T]`, `Pool.FanOut[T]`, `Dataset.Section[T]`. Interfaces stay non-generic in their
methods.

### Python parity gap

None of the packages above has a Python mirror yet, and `techai-webutils` is unchanged by them:
`clients/codebase`, `clients/{git,helm,kubectl,docker,terraform,process,session,toolchain}`,
`clients/aws/*`, `clients/client` (`Output`, `SplitNUL`, `Call`), `foundation/{codebase,yamlq,okf,
stepcache,report,gen,scaffold,impact,pins,dataset,schemagen}`, `services/{probe,toolrun}`,
`execution/{ops,runs}` and `transport/{cobra,mcp,cron}`. Go has no op-service tier: an operation
runs through `execution/ops`. Python keeps `core/interfaces/op_service.py`.

### Go parity gap

The AI call-path tiers are Python-only: the model call path runs in the Python retrieval
service, so no Go consumer exists yet. Each is recorded here until a Go consumer needs it:

- **Evals:** `core/interfaces/{dataset,evaluator,grader}.py`, `clients/dataset`
  (`stub`, `jsonl`, a supplied `aiops`), `clients/grader` (`stub`; `parse`, parse quality
  against golden blocks; `ranking`, recall@k, nDCG@k and reciprocal rank) and `services/evaluator.py`
  (`ThresholdEvaluator`). Go's `foundation/dataset` (the YAML data loader) is unrelated.
- **Safety:** `core/interfaces/safety.py` (`SafetyCheck`, `SafetyResult`, `blocked_error` →
  `FORBIDDEN`) and `clients/safety` (`noop`, `regex`, and the lazily loaded `comprehend` and
  `bedrock_guardrails`, whose aiobotocore client opens on the first scan).
- **Semantic cache:** `core/interfaces/semantic_cache.py` (`SemanticCache` extends
  `CacheInvalidator`; `CacheNamespace` partitions by workspace, embedding space and scope) and
  `clients/semantic_cache` (`stub`; `redis` stores payloads in the injected KV cache and keys
  similarity with a cosine KNN index on the same server, so it needs a search module: RediSearch
  (Redis Stack) or Valkey Search (Valkey 9, e.g. ElastiCache); keys lead with the workspace so `invalidate_prefix(<workspace>)` evicts one tenant).
  A backend exposes two primitives, `exact` (the normalized repeat, no embedding) and `nearest`
  (the nearest neighbour, no threshold). `clients/semantic_cache/tiers` chains config-selected
  tiers over them (`exact`, `semantic`, and `verified`, a band confirmed by a `CacheVerifier` from
  `clients/semantic_cache/verifier`). `clients/semantic_cache/admission` decides which queries
  are cached at all.
- **Usage tap:** `clients/decorators/usage_tap.UsageTapProvider` (see Decorators).
- **Object tags on upload:** Python's `StorageClient.upload` takes keyword-only `tags`; Go's
  `StorageObjects.Upload` does not, since only the Python Bedrock sink (`object_tags`) tags
  what it writes.
- **Model gateway:** `core/interfaces/model_gateway.py` (`ModelGateway` extends `LLMProvider`
  with `resolve(RouteContext)`; `RouteDecision` in a request-scoped slot read through
  `current_route_decision()`) and `clients/model_gateway` (`passthrough` wraps one provider built
  by the required, injected `provider_factory`, e.g. `clients.llm.new_llm_from_config`; `routed` filters, plans and executes over a model catalog).
  `resolve` is async because planning may consult a classifier or a remote planner.
  `AiSpanEnricher` keys `gen_ai.system` on a gateway's `wrapped_provider`.
- **Routing:** `core/types/routing.py` (capabilities, endpoint refs, routes, `RoutingPolicy`,
  plans) and `core/interfaces/routing.py` (`RouteClassifier`, `CandidateRanker`, `RoutePlanner`,
  `RoutingPolicyStore`, `RouteDecisionSink`). `clients/model_gateway/routed` holds the
  boot-validated catalog (each endpoint's `LlmConfig`, `fallback_model` stripped), the
  deterministic filter, the `config` policy store, `RefreshingPolicyStore` (a synchronous
  store over a snapshot an injected async loader refreshes on an interval; workspace, then
  tenant, then a fallback store; a failed refresh keeps the last snapshot and counts
  `routing_policy_refresh_total{outcome}`), which a caller passes as `policy_store` to
  `new_model_gateway_from_config` in place of `routed.policy_store`, and the stage `Kind`s:
  classifier `stub` /
  `embedding` (exemplars embedded once at the gateway's `__aenter__`; an embedder outage at
  request time degrades to the step's route), ranker `static_order` / `cost_aware` (objective,
  hard caps that drop, quality floor, budget bias from the injected `TokenLedger`; every
  ranker is a `PricedCandidateRanker`, whose `rank_priced` costs from a table passed per call, so
  a remote planner whose prices arrive with each request builds one ranker at boot), planner
  `local` / `overlord` (a remote `RoutingPlan` through the product's injected `RouteTransport`;
  the `PlanRequest` carries the context's team and user for attribution; with the service
  token, a deadline, one retry on `UNAVAILABLE` and a breaker; a timeout, an error, an open
  breaker or a plan naming an unoffered model fails open to `local`, counted as
  `routed_planner_fail_open_total{cause}`), decision sink `noop` / `analytics` (`route_decisions` facts). `ChainExecutor` runs the
  plan: bounded attempts, an `AsyncCircuitBreaker` per endpoint, failover on transient errors
  only, streams before their first item only. A `routed.shadow` block plans alongside the live
  call in bounded background tasks and is never served. `clients/model_gateway/routed/settings`
  holds typed, frozen pydantic models of the catalog (`CatalogModelSetting`), routes (`RouteSetting`) and stage fields (`RoutedStagesSetting`,
  whose `routed_config` assembles a `RoutedConfig`), so a consumer's settings load refuses a bad
  entry naming its path. `clients/embedding/memo.MemoizedEmbedding`
  embeds a text once per request, so the classifier and the semantic cache share one call.

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
