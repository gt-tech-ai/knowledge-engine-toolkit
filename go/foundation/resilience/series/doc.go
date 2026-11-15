// Package series records the resilience_* metric series: retries, rejections,
// timeouts, breaker state, the adaptive bulkhead limit, the throttle ratio, hedges, dead
// letters and duplicates. Its Recorder is generated from
// taxonomy/resilience-metrics.yaml, which renders the Python twin
// (techai_webutils.foundation.resilience.series) too, so both languages export the same
// names, labels and label values (ARCHITECTURE.md#resilience-metrics).
//
// Two files are hand-written. observer.go: Observer bundles a Recorder with the logger a
// component's Warn lines go to and the tier and name they carry; it is what the
// decorate layers, the circuit breaker and the other primitives report through.
// retries.go: RetriesDone, the one rule for counting a finished retried call, shared by
// the decorate retry layer and the standalone retrier. retries.go imports
// foundation/resilience/budget to tell a budget refusal from an exhaustion; the
// generated file imports only core/interfaces.
package series
