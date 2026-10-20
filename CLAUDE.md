# CLAUDE.md

Guidance for coding agents working in this repository: the design patterns every change follows,
in Go (`go/`) and Python (`python/techai_webutils/`). [ARCHITECTURE.md](ARCHITECTURE.md) is the
authority and the detailed spec; cite its sections (`ARCHITECTURE.md#<section>`) from code
comments. [CONTRIBUTING.md](CONTRIBUTING.md) has the ground rules and every local check. This file
is the working summary: which pattern to reach for, and the reference code to copy.

Paths below follow ARCHITECTURE.md's convention: a Go path is relative to `go/`, a Python path to
`python/techai_webutils/src/techai_webutils/`.

## The one idea

Everything is an implementation detail behind an interface. The contract lives in `core`; the
implementation is selected by configuration (a `Kind`), injected explicitly through constructors,
and wrapped by decorators for every cross-cutting concern. Swapping a backend is a config change,
and turning a decorator on for one component is a config change too.

Every pattern below is that idea applied to one concern. When a case is not covered here, derive
it from the idea and the nearest reference implementation; a deliberate exception is documented
in ARCHITECTURE.md, never left implicit.

## What this repo is (and is not)

A generic substrate library. It holds contracts, mechanisms and adapters any service stack can
reuse. It holds no product code: no domain event catalogs, authorization models, prompts,
schemas or deployables. A consumer plugs its policy into a seam here (an injected callable, a
`backends` mapping, an event registry). The repo is self-contained: no consumer names, paths,
design documents or tracker ids anywhere, including comments and tests.

## Pattern 1: layers, imports downward only

| Go | Python |
| --- | --- |
| `core` → `foundation` → `clients` → `stores` → `repos` → `services` → `pipelines` → `workflows` → `transport` | `core` → `foundation` (+ `execution`) → `clients` → `repos` → `services` → `pipelines` → `workflows` → `controllers` |

- A package imports its own layer or a lower one, never a higher one and never a sibling family
  in the same tier (hand it a builder instead). `go/execution` imports only `core`; `go/helpers`
  is a leaf.
- `depguard` in `.golangci.yml` enforces the Go layer order. Review enforces the same-tier rule
  and the Python direction.
- Generic codecs used by several tiers belong in `foundation` (`foundation/jsonapi`,
  `foundation/convert`), not at an edge.

## Pattern 2: swappable components (Kind + Config + factory)

Every backend is a `Kind`, a data-only `Config` carrying the kind, and a factory that builds the
selected backend and fails loudly on an unknown kind.

- **Go clients.** One family root file per family holds the `Kind` constants, `Config` (with
  `Name`, the client's name in the decoration config), `Deps` (runtime collaborators, including
  `decorate.Decorations`), a `client.Kinds[Config, Deps, T]` table and `NewFromConfig(ctx, cfg,
  deps)`. `Kinds.Build` also runs the backend through the client tier. Reference:
  `clients/storage/` (`storage.go`, `decorated.go`, `memory/`, `s3/`).
- **Go foundation.** `NewFromConfig(cfg)` / `New(kind, opts...)`, as in `foundation/logger`.
- **Python.** `new_<type>_from_config(config)` with a `StrEnum` kind. Newer factories raise a coded
  `AppError(INVALID_INPUT)`. Heavy optional dependencies are imported lazily inside the selected
  branch.
- **Stub first.** The default kind is an in-process stub, memory or no-op backend, so a
  composition root boots with zero infrastructure. The stub is shipped production code, not a
  test double.
- **Consumer-supplied kinds.** When a backend must call the consumer's own service, the factory
  takes `backends: Mapping[str, Callable[[Config], T]]` and matches `Config.kind` against built-ins
  first. Reference: `clients/token_ledger/builder.py`.
- One backend per sub-package beside the factory.

## Pattern 3: small role interfaces, composed

- `core/interfaces` holds role interfaces (`Reader`, `Lister`, `Writer`, `Exister`, `Lifecycle`,
  `HealthChecker`, …); larger contracts and role compositions (`ReadListExistStore`,
  `CRUDNoListRepository`, …) embed them and live in `core` too.
- An interface declared outside `core` embeds a core interface, or carries one of the verbatim
  tags `// SDK seam`, `// sealed union` or `// interface-composition exemption — <reason>`.
- An opt-in capability composes rather than widens (`DynamicConsumer` embeds `MessageConsumer`).
  Python Protocols compose the same way.

## Pattern 4: dependency injection, no globals

- Collaborators arrive through constructors, `With*` builders or functional options
  (`foundation/options.Option[T]`). There are no `init()` functions, service locators or
  singletons. The OTel tracer backend is the one process-global.
- The consumer's `main` is the single composition root. It loads config, calls the factories,
  builds one `Decorations`, and registers lifecycle clients with `foundation/lifecycle.Manager`
  (start in order, stop in reverse).
- Constructors do no I/O; a lifecycle client connects in `Start`. Python backends are
  `ManagedResource` async context managers.

## Pattern 5: decoration, one engine for every tier

Cross-cutting concerns are decorators applied around a component, never code inside it. Both
languages share one engine:

- **Middleware.** `foundation/decorate` defines each concern once as an `OpMiddleware` (recovery,
  tracing, metrics, logging, auth, timeout, retry, circuit breaker, bulkhead, rate limit) and
  provides `chain` and `exec_op`.
- **Tier.** A `Tier` is a tier's name and its layers, outermost first, as data.
  `Tier.Subject(name, deps)` builds the chain for one component; `Subject.Exec(op, fn)` runs one
  operation through it. A nil collaborator or a zero timeout leaves its layer out; recovery needs
  none.
- **Deps from config.** `Decorations` turns the `decoration` config section plus the process
  `Collaborators` (logger, metrics, tracer, authorizer) into a component's `Deps`:
  `Decorations.Deps(<tier>.Tier(), name)` in Go, `decorations.deps(tier(), name)` in Python. Each
  tier has `defaults` and per-component `overrides` keyed by dotted name, cascading from each
  prefix to the full name. The **order** stays in code; **which layers run and how they are
  tuned** is config. Every base resolves its component as `<name>` and each contract operation as
  `<name>.<op>`, and the root calls `Verify(prefixes…)` / `verify(*prefixes)` (or
  `VerifyExplicit` / `verify_explicit` over a generated file) so an unused override, a missing
  entry or an unsupported layer fails boot. `Inventory()` / `inventory()` lists every resolved
  name with its layers and source keys.
- **Base per tier, built from the contract.** Each tier package has `<tier>.go` (`Tier()`, `Base`,
  `NewBase[C](decs, name, …) (Base, error)`), and each entity tier an `Entity` base
  (`entity.go`) that embeds `Base` and serves the entity's roles over the next tier;
  Python's is `XBase(contract, decorations, name)`. Each method of the contract `C` is one
  operation, decorated as `<name>.<snake_case method>`; a component embeds the base, implements
  each method through `Exec` under that op, and calls the next tier. Running an undeclared op
  fails `INTERNAL`. Wire-level op names (RPC procedures, topics) use `client.NewWireBase` /
  `ClientBase.wire`. Tests use `fixtures.Decorations()` or `decorate.Fixed(deps)` /
  `Decorations.fixed(deps)`. Never hand-wrap a concern.
- **Not a layer: hedging.** A hedge races a second attempt against a slow one, so it is only
  safe for a read the caller knows is idempotent. It is a primitive the app composes around
  that call (`foundation/resilience/hedge`, `reswire.NewHedge` in Go;
  `foundation.resilience.hedge.hedger_from_config` in Python), never switched on per tier.

| Tier | Order (outermost first) | Go base | Python base |
| --- | --- | --- | --- |
| Controller | Tracing → Metrics → Logging → Recovery → RateLimit → Bulkhead → Auth → Timeout | `transport.Base` (`Handler`, `Exec`) | `controllers.ControllerBase` (`handler`, `exec`) |
| Client | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging | `client.Base` (`Exec`, `ExecOnce`, `Do`, `DoOnce`, `Stream`, `Open`); `NewWireBase` | `clients.client.ClientBase` (`exec`, `exec_once`); `ClientBase.wire` |
| Workflow | Recovery → Logging → Tracing → Metrics → Timeout | `workflow.Base` (`Exec`, `ExecTx`, `InTx`); `workflow.Entity` | `workflows.WorkflowBase` (`exec`, `exec_tx`, `in_tx`, `crud`) |
| Pipeline | Recovery → Logging → Tracing → Metrics → Timeout | `pipeline.Base` (`Stage`, `FanOut`; `Stage.Then`); `pipeline.Entity` | `pipelines.PipelineBase` (`stage`, `fan_out`, `crud`; `Stage.then`) |
| Service | Recovery → Tracing → Metrics → Logging → Auth → Timeout | `service.Base` (`Exec`); `service.Entity` | `services.ServiceBase` (`exec`, `crud`) |
| Repository | Tracing → Metrics → Logging → Timeout → CircuitBreaker → Retry | `repository.Base` (`Exec`, `ExecOnce`); `repository.Entity` (with `Cache`); `repos/outbox.New` | `repos.RepositoryBase` (`exec`, `exec_once`, `crud`) |
| Store (Go only) | Tracing → Metrics → Logging → Timeout | `store.Base` (`Exec`, `Write`, `Client`); `store.Entity` (from `store.Funcs`) | — |
| Job | Recovery → Logging → Tracing → Metrics → Timeout | `job.Base` (one per job: `Func`, `Local`, `Wrap`) | `execution.job.JobBase` (`func`, `wrap`) |

Conventions every tier shares, identical in both languages:

- Span `<name>.<op>` with `<tier>.name` / `<tier>.operation`; the span kind is the tier's
  (SERVER for controllers, CLIENT for clients, PRODUCER for a publish and CONSUMER for a
  message handler).
- Series `<tier>_operations_total`, `<tier>_errors_total`, `<tier>_operation_duration_seconds`,
  labelled `{<tier>, operation}`.
- Operation names are Go's: `Get`, `List`, `Create`, `Update`, `Delete`, `Exists` for CRUD,
  `Execute` for a stage, workflow or job, `Run` for an operation.
- Inner logging layers log failures at Debug; the controller tier is the edge (`Tier.Edge` /
  `Tier.edge`) and logs a failure once, at Warn for a client fault and at Error otherwise.
  Recovery logs a panic even where the logging layer is off (`Deps.PanicLogger` /
  `Deps.panic_logger`).
- A Create is never retried (its commit may have landed). A message handler runs as
  `<name>.handler`, never retried, broken or bulkheaded; the broker's redelivery is the retry.
- A consumer type embeds the Go base, or subclasses `DelegatingRepository` /
  `DelegatingCrudService` in Python, and runs each custom op through `Exec` / `exec`, so it
  shares the CRUD path's names and authorization action.

## Pattern 6: protocol edges lay the tier out as interceptors

A server or RPC client does not get its own decorator stack. It lays the controller or client
tier out in the framework's interceptor shape.

- **Servers.** `transport.Interceptors` (Go) and `controllers.interceptors.lay_out` (Python) walk
  the controller tier. A layer the framework does better natively (a trace continued from
  metadata, metrics by status code, a status-aware access log) comes from a native interceptor;
  each run of the remaining layers becomes one interceptor; authenticating interceptors run just
  before Auth (`Edge.Authn` / `Edge.authn`), and validation runs innermost (`Edge.Inner` /
  `Edge.inner`). A stream runs without the timeout. A handler's own status reaches the caller
  unchanged; a rejection the tier produced becomes the status of its code with only that code's
  client-safe message. References:
  `transport/connect/interceptors.ServerOptions`, `transport/grpc/interceptors.ServerOptions`,
  `controllers/grpc/interceptors.server_interceptors`.
- **RPC clients.** Each unary call runs through the client tier as op `pkg.Service/Method`; every
  attempt carries the trace context and the service token. A server status is coded for the tier
  (`clients/rpc/wire` in Go, `grpc_error_to_app_error` in Python) and handed back verbatim; a
  tier-made failure becomes the status of its code. `rpc.NewFromConfig` and
  `new_rpc_channel_from_config` install it.

## Pattern 7: errors carry a code

- Go: `core/errors` `New(code, msg)` / `Wrap(err, code, msg)`. `fmt.Errorf` is banned in
  production code by `forbidigo`. Python: `AppError(code, message, cause=…)` or a subclass.
- Retryability, transport status and breaker accounting derive from the code, never the message.
  Only transient codes (`TIMEOUT`, `UNAVAILABLE`) retry; `RESOURCE_EXHAUSTED` never does.
- SDK errors are translated to coded errors at the client boundary
  (`foundation/resilience/{grpc,aws}_boundary.py`, `clients/rpc/wire`). Messages are sanitized at
  the transport edge.
- `go/core/errors/testdata/codes.json` pins each code's gRPC/HTTP/transient row for both
  languages.

## Pattern 8: config selects, the composition root injects

- Layered YAML (`base.yaml` → `{env}.yaml` → `secrets.yaml` → env vars) into typed sections, each
  with defaults and a coded validation. Business logic never reads the environment.
- The `decoration` section is shared by Go and Python: the same keys and the same defaults
  (`DefaultConfig` / `default_config`). Durations are Go strings (`"30s"`) in both
  (`foundation/config/duration.parse_duration`). With no section at all, today's behavior holds.

## Pattern 9: tooling reads one codebase, operations run everywhere

A check, generator or tool run reads the tree through an `interfaces.Codebase`, never `os` or
`filepath.Walk`: the snapshot memoizes every read and parse for the run, and the `memory` kind
makes the check testable without a disk. Policy that names a consumer's files, schedules or
tools is data the consumer passes in (a `pins.Policy`, a `ToolEntry` list, a `DocsConfig`), never
a literal here. A developer operation is registered once in `execution/ops` and exposed by a
front-end (`transport/cobra`, `transport/mcp`, `transport/cron`) under an exposure policy.

Generic methods on concrete types are allowed (Go 1.27) where a typed result would otherwise
need a type switch (`Snapshot.Memo[T]`, `Pool.FanOut[T]`, `Dataset.Section[T]`); interface
methods stay non-generic, so a contract can still be mocked.

## Go and Python parity

Python mirrors Go one-to-one where the languages allow it: the same tier names and orders, the
same op, span, series and log names, and the same config keys. When you change one language's
tier, pattern or public behavior, change the other in the same change, or record the gap in
ARCHITECTURE.md. Known differences are deliberate:

- Python has no store tier, no synchronous pipeline or workflow variants, and no Redis
  messaging kind.
- Python servers run every RPC shape as one coroutine (a stream is written with `context.write`),
  so the tier wraps the whole stream.
- Python consumer-supplied kinds use a `backends` mapping where Go uses a kinds table.
- The repository tooling substrate (codebase, probes, tool runs, generators, the command-line
  and AWS clients, operations and their front-ends) is Go-only; ARCHITECTURE.md lists the
  packages. Python keeps `core/interfaces/op_service.py`; Go has no op-service tier.

## Testing

- Black-box tests only: Go under `go/tests/{unit,integration,fixtures,mocks}`, Python under
  `python/techai_webutils/tests/{unit,integration}`. Assert observable behavior, not internals.
- Unit tests mock only the external boundary: mockgen-generated mocks in Go
  (`go/tests/mocks`, regenerated with `go generate ./go/tests/mocks/...`), `unittest.mock` bound
  with `spec=` to the core interface in Python. No hand-rolled fakes.
- Integration tests run the real dependency in a testcontainers container
  (`//go:build integration`; `@pytest.mark.integration`).
- Each test has a doc comment saying why it matters and what it asserts.
- Python's unit run enforces 90% coverage. The Python unit suite's shared `observed` fixture
  (`tests/unit/conftest.py`) records the spans, series and log lines a decorated tier emits.

## Gates

There is no repo CLI here; run the tools directly (CONTRIBUTING.md#local-checks has the full
list).

```sh
# Go (repo root)
golangci-lint fmt ./go/... && golangci-lint run ./go/...
go test -race ./go/...
INTEGRATION=1 go test -tags=integration ./go/...   # needs Docker

# Python (python/techai_webutils)
uv run ruff format . && uv run ruff check .
uv run basedpyright
uv run pytest -m "not integration"                 # 90% coverage gate
uv run pytest -m integration --cov-fail-under=0    # needs Docker
```

A change to the Python package's `src/` or `pyproject.toml` needs a version bump, and every
release needs a `CHANGELOG.md` section (ARCHITECTURE.md#versioning-and-releases).

## Anti-patterns

- A concern (retry, timeout, logging, metrics, tracing) inlined in a backend or a handler, or a
  hand-ordered builder or proxy stack, instead of the tier's base.
- An implementation chosen by `if` or by import instead of a `Kind` factory; a factory that
  silently falls back on an unknown kind.
- A config or env read outside the config layer; a hard-coded timeout or limit that belongs in
  the `decoration` section.
- An uncoded error crossing a package boundary.
- An upward or same-tier import; product vocabulary or a consumer reference anywhere in the repo.
- A Go-only or Python-only change to shared behavior, with no parity change and no documented
  difference.
