package series

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
)

// RetriesDone records one call a retrier finished with err after the given attempts: one
// retry per attempt after the first; then, when err is the retry budget refusing
// another attempt, a budget_exhausted rejection and nothing more; else, when err is a
// failure after at least one retry, an exhaustion. It reports whether it counted an
// exhaustion, so a caller with a logger can warn; the count happens even on a nil
// Recorder, which records nothing. The decorate retry layer and a standalone retrier
// share it, so both count a call the same way.
func (r *Recorder) RetriesDone(operation string, attempts int, err error) bool {
	for range attempts - 1 {
		r.Retried(operation)
	}

	if err == nil {
		return false
	}

	if apperr.StdIs(err, budget.ErrRetryBudgetExhausted) {
		r.Rejected(operation, ReasonBudgetExhausted)

		return false
	}

	if attempts <= 1 {
		return false
	}

	r.RetryExhausted(operation)

	return true
}

// RetriesDone records one retried call through the Recorder (Recorder.RetriesDone) and,
// when it counted an exhaustion, writes one "retries exhausted" Warn naming the
// operation and the attempts.
func (o *Observer) RetriesDone(operation string, attempts int, err error) {
	if o.Record().RetriesDone(operation, attempts, err) {
		o.Warn("retries exhausted", "operation", operation, "attempts", attempts)
	}
}
