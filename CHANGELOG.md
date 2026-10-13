# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [Unreleased]

### Added

- Python parity with the Go repository, service, pipeline and workflow tiers:
  `foundation/decorate` (`OpMiddleware`, `chain`, `exec_op` and the recovery, tracing, metrics,
  logging, auth, timeout, retry and circuit-breaker middlewares); `repos.repo_op_chain` +
  `RepoOpDeps` and `services.service_op_chain` + `ServiceOpDeps`, which decorate custom repository
  queries and domain service operations with the tier's CRUD stack; `DelegatingRepository` and
  `DelegatingCrudService`, so a consumer type inherits decorated CRUD and adds only its custom
  ops; `ServiceBuilder.with_validation`, `with_metrics` and `with_tracing`; `pipelines.then`
  (Go `pipeline.Then`); and `workflows.tx_workflow` (Go `workflow.NewTxWorkflow`).

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

[Unreleased]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.6...HEAD
[0.0.6]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.5...v0.0.6
[0.0.5]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/releases/tag/v0.0.1
