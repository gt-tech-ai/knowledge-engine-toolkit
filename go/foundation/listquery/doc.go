// Package listquery is the pure, ORM-free half of the list-query SDK
// a per-resource filter allow-list (Map) + sort allow-list
// (Set), a JSON filter-DSL parser that compiles a client filter string into the
// core/types Filter contract (gated by the allow-list so only allow-listed fields
// with type-checked values reach a query), and the shared Compile/CompileOrder
// fold that walks that IR through a core/interfaces.QueryBackend algebra. It
// imports only core/types + core/interfaces + core/errors + stdlib — no ORM: each
// datastore backend (Ent now; Mongo/Elasticsearch future) is a repos-tier
// implementation of the algebra the fold drives.
package listquery
