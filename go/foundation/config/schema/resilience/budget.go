package resilience

import apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"

// BudgetConfig tunes the retry budget (budget.NewBudget), which caps the total
// retries a client may spend so a downstream outage cannot amplify load.
type BudgetConfig struct {
	// MaxRetries is the retry-budget ceiling shared across calls (0 = no budget).
	MaxRetries int32 `mapstructure:"max_retries"`
}

// DefaultBudgetConfig returns a conservative default retry budget.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{MaxRetries: 100}
}

// Validate rejects a negative budget.
func (c BudgetConfig) Validate() error {
	if c.MaxRetries < 0 {
		return apperr.InvalidInput("resilience.budget.max_retries must be >= 0")
	}
	return nil
}
