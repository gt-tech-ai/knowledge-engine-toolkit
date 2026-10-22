package interfaces

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Prompter asks the operator a yes/no question.
type Prompter interface {
	// Confirm asks question and reports whether the operator answered yes. A front-end
	// that cannot get an answer returns NonInteractiveError rather than a silent "no",
	// so an unattended run fails loudly.
	Confirm(ctx context.Context, question string) (bool, error)
}

// NonInteractiveError is the CodeInvalidInput error a Prompter returns when no answer
// can be obtained (stdin at EOF, a front-end with no operator); it names --yes, the flag
// that skips the question.
func NonInteractiveError() error {
	return apperr.New(apperr.CodeInvalidInput,
		"no answer to confirm (non-interactive run): pass --yes")
}
