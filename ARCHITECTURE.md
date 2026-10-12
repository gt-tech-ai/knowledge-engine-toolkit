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
decorator mechanisms, lifecycle) → `go/clients` (external systems) → `go/repos` → `go/services` →
`go/pipelines` → `go/workflows` → `go/transport` (REST/RPC edges).

Outside the ladder: `go/execution` (batch fan-out; imports only `core`), `go/helpers` (leaf
utilities), and `go/tests` (may import anything). No package imports a higher layer. Integration
families in a tier don't import each other (`clients/connector` is handed a storage builder rather
than importing `clients/storage`); the shared clients-tier pieces are `clients/decorators` and
`clients/interceptorcore`. `.golangci.yml` enforces the layer order and the `go/helpers` leaf rule
with `depguard`; the `go/execution` and same-tier rules have no lint rule, so review holds them.

Python mirrors the tiers: `core` → `foundation` (+ `execution`) → `clients` → `repos` →
`services` → `pipelines` → `workflows` → `controllers` (the transport edge). Imports point
downward, except one type-checking-only import in `foundation/resilience/sqs_dlq.py`.

## Swappable components

A swappable component is a **`Kind`** (Go `type Kind int` + `iota` + `String()`; Python `StrEnum`),
a **`Config`** carrying the kind and backend settings, and **a factory** that fails loudly on an
unknown kind:

- Go: `New(kind, opts...)` / `NewFromConfig(cfg)` (`foundation/logger`, `foundation/metrics`, …)
  or `NewFromConfig(ctx, kind, cfg)` at a client tier root (`clients/storage`,
  `clients/messaging`); an unknown kind is a `CodeInvalidInput` error.
- Python: `new_<type>_from_config(config)` (or `<type>_from_config`, e.g. `executor_from_config`);
  an unknown kind raises `ValueError` in the older tiers and a coded `AppError(INVALID_INPUT)` in the
  newer ones (`clients/audit`, `clients/facts`, `clients/token_ledger`).
- **Consumer-supplied kinds.** A backend that must call a consumer's own service (which the KE may
  not import) is injected: the factory takes `backends: Mapping[str, Callable[[Config], T]]`, and
  `Config.kind` is a `str` matched against the built-in kinds first, then the `backends` keys
  (`clients/token_ledger/builder.py`, `clients/audit/builder.py`). The kind stays a config value; the KE stays product-free.

One backend per sub-package beside the factory: `clients/storage/{s3,memory}`,
`clients/lock/{local,redis}`, `foundation/logger/{zap,stdlib}`, `clients/llm/{bedrock,ollama,stub}`.
Python imports heavy or optional dependencies lazily in the selected branch, so the `ray`,
`postgres`, `parsing` and `langdetect` extras load only when chosen (`clients/lock/builder.py`).

## Interface composition

`go/core/interfaces` holds small role interfaces (`Reader`, `Writer`, `Lister`, `Exister`,
`Lifecycle`, `HealthChecker`, …) that larger contracts (`Repository`, `Service`, `Client`) compose.
An interface declared **outside** `core` embeds a core interface (`repos/repository.ReadListExistStore`
embeds `Reader`/`Lister`/`Exister`; `clients/rpc.RPCClient` embeds `interfaces.Client`). Opt-in
capabilities compose rather than widen (`DynamicConsumer` embeds `MessageConsumer`); Python
Protocols do the same (`StorageClient` composes `ManagedResource`).

An out-of-core interface that cannot embed one carries a doc-comment tag saying why:

- `// SDK seam` — mirrors a third-party SDK surface (AWS S3/SQS/Secrets Manager,
  `grpc.ClientConnInterface`) so the adapter can be driven by a generated mock.
- `// sealed union` — a closed implementer set standing in for a sum type (`core/types.Filter`).
- `// interface-composition exemption — <reason>` — a mechanism primitive or narrow consumer-side
  port, not a data-access surface (`foundation/decorator.Executor`, `execution/engine.Step`, …).

Every interface under `go/` outside `core` (tests and mocks aside) embeds a core interface or
carries a tag. Keep the tags verbatim.

## Dependency injection

Collaborators arrive as constructor arguments, `With*` builder calls, or functional options
(`foundation/options.Option[T]`); the Go tree has no `init()` functions or service locators (the
OTel tracer backend is the one piece that installs process-global state). KE is a library: the
consumer's `main` is the single composition root that builds config, calls the factories, applies
decorators, and registers clients with `foundation/lifecycle.Manager` (start in order, stop in
reverse). Lifecycle-managed clients connect in `Start`, not their constructors (`repos/adapters/gorm`,
`clients/database/postgres`). Python backends are `ManagedResource` async context managers; the
documented exception is logging, which structlog configures once and seams resolve by name.

## Decorators

Cross-cutting concerns wrap business logic and backends, never live inside them. A nil
collaborator or zero timeout skips its layer (the service builder always adds recovery).

- **Go — fluent builders** (`NewBuilder(base, name).WithLogging(l).WithTracing(t).Build()`)
  returning the wrapped interface. Shared bodies: `foundation/decorator` (Execute-shaped tiers),
  `foundation/decorate` (`OpMiddleware`, `Chain`, `Exec[R]` for custom repository/service ops), and
  `clients/decorators` (the client-boundary `Stack`: `Run`, `RunStream`, `StackFromConfig`).
  `foundation/decorator` (Go and Python) exposes `Unwrap` so tests can reach the wrapped unit.
  Its generic `CircuitBreaker(inner, cb, onOpen)` codes only an open-breaker rejection as
  `UNAVAILABLE`; the operation's own error passes through with its code.
- **Go — workflow helpers.** `decorators.Decorate(wf, op, logger, metrics, tracer, cfg)` is the
  standard workflow stack (the builder with `cfg.Timeout` from `schema/workflows.Load`), and
  `workflow.NewTxWorkflow(txMgr, pipe)` runs a command pipeline inside one transaction.
- **Python — `__getattr__` proxies** in `clients/decorators/proxy.py` (`LoggingProxy`, `RetryProxy`,
  `CircuitBreakerProxy`, …) apply their concern around each awaited call;
  `new_client_stack_from_config` composes them. `RetryProxy` (async) and `retry_transient_async`
  wait out a server pushback (`details["retry_after_ms"]`, read by `retry_after_s`) before the next
  attempt, capped by `max_pushback_s` / the retry's max delay. Tier builders reuse
  `foundation/decorator.py`.
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
- **Python — client RED metrics.** `clients/decorators/metrics_proxy.MetricsProxy` (the client
  stack's Metrics layer) emits `client_operations_total{client,method,outcome}`,
  `client_errors_total{client,method,code}` and `client_operation_duration_seconds{client,method}`.
  A streamed call is measured at open only, not over the stream's lifetime.

Inner logging layers (client stack, repository, service, pipeline, workflow) log failures at Debug
so the outermost recovery/transport seam logs the Error once; the lock and replay-buffer loggers
still log at Error.

### Decorator order

Outermost → innermost:

| Stack | Where | Order |
| --- | --- | --- |
| Client boundary | `go/clients/decorators` | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging |
| Job | `go/clients/jobs/decorators` | LeaderElection → RateLimit → Retry → Timeout → Tracing → Metrics → Logging |
| EventHandler | `go/clients/messaging/decorators` | Dedup → DeadLetter → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging |
| Lock | `go/clients/lock/decorators` | Tracing → Metrics → Logging → Timeout → CircuitBreaker → Retry |
| Replay buffer | `go/clients/replaybuffer/decorators` | Tracing → Metrics → Logging → Timeout |
| Cache | `go/clients/cache/decorators` | Metrics → Timeout → CircuitBreaker |
| Analytics store | `go/clients/analytics/decorators` | Recovery → Metrics → Tracing → CircuitBreaker → Timeout (the Cassandra session's writes ride the Client boundary stack) |
| Outbox sink | `go/clients/outbox/decorators` | the Client boundary stack around each SDK call (`SQSAPI`, `S3API`), with SDK faults coded so only throttling/server faults retry |
| Prometheus query | `go/clients/prometheus` (`DecorateDoer`) | the Client boundary stack around each HTTP GET, op `prometheus.<endpoint>` (retryable; the capped body is read inside the attempt; 429/502/503/504 are coded transient) |
| Connect server | `go/clients/transport/connect/interceptors` | Recovery → RetryBudget → RateLimit → Bulkhead → Metrics → Tracing → Logging → ServiceAuth → Auth → caller-supplied (`WithInterceptors`: the consumer's principal and tenant-scope interceptors) → Validate |
| Connect/gRPC client | + `go/clients/rpc/grpc/interceptors` | Metrics → CircuitBreaker → Retry → Timeout → Tracing → Logging (gRPC appends ServiceAuth) |
| gRPC streaming client | `go/clients/rpc/grpc/interceptors` (`StreamingClientBuilder`) | Timeout → Metrics → CircuitBreaker → Retry → Tracing → Logging → ServiceAuth |
| gRPC server | `go/clients/rpc/grpc/interceptors` | Recovery → RateLimit → Bulkhead → Metrics → Tracing → Logging |
| Repository | `go/repos/repository/decorators` | Tracing → Metrics → Logging → Timeout → CircuitBreaker → Retry → Caching |
| Service | `go/services/service/decorators` | Recovery → Validation → Tracing → Metrics → Logging → Authorization → Timeout |
| Pipeline, Workflow | `go/{pipelines/pipeline,workflows/workflow}/decorators` | Recovery → Logging → Tracing → Metrics → Timeout |
| Transport handler | `go/transport/decorators` | Recovery → Logging → Metrics → RateLimit → Timeout |
| Execution job | `go/execution/job/decorators` | Tracing → Metrics → Logging → Recovery |
| Python client | `clients/decorators/proxy.py` | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging (Metrics only with an injected `MetricsProvider`) |
| Python EventHandler | `clients/decorators/event_stack.py` | Dedup → DeadLetter → Retry → CircuitBreaker → Timeout |
| Python Job | `clients/decorators/job_stack.py` | LeaderElection → RateLimit → Retry → Timeout |

In the client stack Retry applies only to `Retryable` ops (`RunStream` never retries) and sits
outside the breaker, so each attempt is observed. The gRPC streaming client puts Timeout
outermost, so one budget bounds the whole retried stream open (not the stream's lifetime); with a
service token it also adds a unary ServiceAuth interceptor for the connection's unary calls.
Custom repository ops (`OpChain`) use the Repository order minus Caching. Tests pin the orders
(`go/tests/unit/clients_wrap_order_test.go` for the client boundary, EventHandler and Job stacks;
`services_wrap_order_test.go`, `clients_lock_decorators_test.go`, `foundation_decorator_test.go`).

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
`KindCassandra`). The Cassandra store sits on `clients/cassandra.Session` — `KindCassandra`
(self-managed, token- and DC-aware) or `KindKeyspaces` (Amazon Keyspaces: TLS + SigV4) — and keeps
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

The transactional outbox is split by tier. `core/interfaces.OutboxStore` (`Claim` leases due rows,
`MarkSent` / `Retry` / `Park` / `Stats`) is supplied by the consumer over its own table;
`core/interfaces.OutboxSink` returns one result per record. `services/outbox.Relay.RunOnce` claims a
batch, sends it in `SendBatch` chunks under a `Concurrency` bound, and finalizes each row by its own
outcome: sent, retried at `now + FullJitter(min(Base·2^attempts, Max))`, or parked on its
`MaxAttempts`th attempt (`outbox_{sent,retried,parked}_total`, `outbox_depth`, `outbox_lag_seconds`
by lane). With `ParkOnPermanent` a permanent failure parks the row at once; a run cancelled mid-send
leaves its unfinished rows leased for the next claim. `LoadRelayConfig` reads a lane's `RelayConfig`
over the defaults. `clients/outbox` builds the sink (`KindStub` default, `KindSQS`, `KindS3`) over
an injected SDK client: SQS routes each row by its `route` attribute through a config route → queue
map with per-queue failure isolation (a `.fifo` queue gets group id `Key`, else `Tenant`, else
`Lane`, and deduplication id `ID`); the sinks keep an SDK error's code (`errors.CodeOr`); S3 writes
one object per row by key template with a SHA-256 checksum (Object Lock) and optional SSE-KMS.
`go/tests/fixtures/outboxtest` is the store conformance suite (`Run`, `RunRouting`) plus a test-only
Postgres reference store.

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

- **Go:** analytics `stub`; lock `local`; outbox sink `stub`; messaging, replay buffer, storage `memory`; secrets
  `env`/`file`; tracer `noop`; metrics no-op when disabled; logger `stdlib`. Jobs' `NewFromConfig`
  currently returns a no-op River enqueuer.
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

Go `go/execution` models batch work: discover → map → fan out → aggregate.

- `engine`: `FanOut[T]` runs over items with bounded concurrency (a slot is acquired before each
  goroutine spawns), recovers panics into `Fail` results, never lets a unit cancel its siblings;
  `RunJobGroup` runs `AnyJob`s concurrently, streaming `StepResult`s to an `ExecutionObserver`.
- `job`: `job.New[T](name, group)` builds a `Job[T]` (a `Discoverer[T]` plus a mapper to
  `WorkUnit`) with concurrency, retry, and warn-only options; `job/decorators` wraps any `AnyJob`.
- `gate`: `gate.NewGate(name).Parallel(…).Serial(…).StopOnFailure().Build()`; `RunGate` runs phases
  in order and aggregates failures into a `CodeQualityFailed` error.

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
  `Workflow[struct{}, struct{}]`. The composition root decorates it (recovery outermost).
- `NewTicker(name, pass, Config, logger)` runs the pass once per `Interval`, plus one at start when
  `RunAtStart` is set. A zero `Interval` runs passes back to back, for a pass that already blocks
  on its source. A failed pass is logged at warn and followed by `ErrorBackoff`. A failure caused
  by the shutdown is not logged, and `Run` returns nil when its context ends.
- `Group` (an `interfaces.Lifecycle`) starts every `Runner` (a `Ticker`, or any `RunnerFunc`) on
  its own goroutine. A runner that returns an error is logged at error and the others keep
  running. `Wait` blocks until all have returned; `Stop` cancels them and waits within its
  context.

## Staged pipelines

Go `pipeline.Then(first, second)` composes two pipelines into one, feeding first's output to
second; an error from first stops the chain and is returned unchanged. Build each stage with
`decorators.NewBuilder` before composing, so every stage records its own span and metrics and a
slow or failing stage is visible by name. `Then` nests: `Then(a, Then(b, c))` is a three-stage
pipeline.

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
