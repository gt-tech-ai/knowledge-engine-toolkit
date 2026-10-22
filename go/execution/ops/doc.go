// Package ops is an operations registry: one set of typed operations that several
// front-ends run the same way, such as a CLI, an MCP server (transport/mcp) and a
// scheduled job (transport/cron).
//
// An operation is a descriptor (name, summary, safety class, lifecycle, timeout),
// usually loaded as data, paired with a Builder that binds it to the front-end's
// environment; Func adapts a plain function over a typed input, whose JSON Schema is
// reflected from the input's json tags. NewFromConfig builds a Registry from the
// builders, the descriptors and one ExposurePolicy: it admits only the operations the
// policy allows (safety-class ceiling, lifecycles, allow and deny lists) and wraps each
// run in recovery → timeout → logging → code mapping. The registry is a single-slot
// bulkhead: one operation runs at a time, and Reserve claims the slot before a run
// starts.
package ops
