// Package resilience provides fault-tolerance primitives as standalone subpackages.
//
// Each subpackage is independently importable and follows the Config/DefaultConfig
// pattern:
//
//   - [retry] — Exponential backoff with jitter (cenkalti/backoff/v5)
//   - [circuitbreaker] — Failure detection and load shedding (sony/gobreaker)
//   - [bulkhead] — Concurrency limiting via semaphore
//   - [budget] — Cross-context retry budget to prevent retry storms
//   - [ratelimiter] — Token-bucket rate limiting (golang.org/x/time/rate)
//
// Usage (paths under github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation):
//
//	import (
//	    ".../resilience/retry"
//	    ".../resilience/circuitbreaker"
//	    ".../resilience/bulkhead"
//	    ".../resilience/budget"
//	    ".../resilience/ratelimiter"
//	)
package resilience
