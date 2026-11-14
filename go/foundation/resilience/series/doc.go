// Package series records the resilience_* metric series: retries, rejections,
// timeouts, breaker state, the adaptive bulkhead limit, the throttle ratio, hedges, dead
// letters and duplicates. Its Recorder is generated from
// taxonomy/resilience-metrics.yaml, which renders the Python twin
// (techai_webutils.foundation.resilience.series) too, so both languages export the same
// names, labels and label values (ARCHITECTURE.md#resilience-metrics).
//
// Observer (hand-written, observer.go) bundles a Recorder with the logger a component's
// Warn lines go to and the tier and name they carry; it is what the decorate layers, the
// circuit breaker and the other primitives report through.
package series
