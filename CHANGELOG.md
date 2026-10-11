# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [Unreleased]

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

- `pipelines/pipeline.Then`: composes two pipelines into one, the first's output feeding the
  second. An error from the first is returned unchanged and the second does not run; each
  stage keeps its own decorators. A nil stage panics.
- `workflows/loop`: background work as decorated workflow passes. `Pass` adapts a
  `func(context.Context) error` into a `Workflow[struct{}, struct{}]`. `NewTicker` runs a pass
  per `Config.Interval` (back to back when zero; `RunAtStart` adds a startup pass), logs a
  failed pass and waits `ErrorBackoff`, and returns nil when its context ends. A negative
  duration is `CodeInvalidInput`. `Group` is an `interfaces.Lifecycle` that runs `Runner`s
  (`RunnerFunc` adapts a function) together and logs one that stops with an error without
  stopping the others; `Stop` cancels and waits within its context (`CodeTimeout` /
  `CodeCanceled`), and a second `Start` is `CodeConflict`.
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

[Unreleased]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/releases/tag/v0.0.1
