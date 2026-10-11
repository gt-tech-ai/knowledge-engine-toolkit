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

- `core/types.FilterNode`: the filter tree with a JSON codec for the listquery grammar, and
  `MaxFilterDepth`.
- `foundation/listquery.ParseFilter`: checks a `FilterNode` against a `Map`; `Parse` is now
  built on it. Integers outside ±(2^53−1) are rejected.
- `testdata/listquery/filters.golden.json`: the shared filter corpus.
- `foundation/chartspec`: a content-addressed node encoding for `ChartSpec`. `Node` and `Hash`
  (SHA-256 over `"chartspec/v1\x00"` and the node's RFC 8785 canonical JSON); `Encode` splits a
  spec into a `Tree` of shared nodes, `Decode` rebuilds it through a `NodeGetter` and verifies
  each node against its hash, and `Diff` reports the changed paths between two roots as
  `Change`s. New dependency: `github.com/gowebpki/jcs`.
- `testdata/chartspec/nodes.golden.json` and `testdata/chartspec/specs.golden.json`: pinned
  node and root hashes.
- `clients/cassandra`: `CassandraConfig.DisableHostDiscovery` pins a cluster reached through
  a proxy, tunnel or port mapping to its one IP-literal contact point, translating every
  address the cluster advertises back to it; `ReconnectInterval` sets how often downed
  hosts are retried.

## [0.0.1] - 2026-10-10

### Added

- Initial release of the Go module `github.com/gt-tech-ai/knowledge-engine-toolkit` and the
  Python package `techai-webutils`: the layered substrate (core → foundation → clients →
  repos → services → pipelines → workflows → transport), its `Kind` + `Config` factories,
  decorator stacks, the execution fan-out engine, and the test fixtures and generated mocks.
  `ARCHITECTURE.md` describes each layer.

[Unreleased]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/gt-tech-ai/knowledge-engine-toolkit/releases/tag/v0.0.1
