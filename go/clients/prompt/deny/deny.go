// Package deny is the Prompter backend for a front-end with no operator (an MCP server, a
// web app, CI): it refuses every question with the --yes guidance instead of blocking.
package deny

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Prompter refuses every confirmation.
type Prompter struct{}

// New returns a Prompter that refuses every confirmation.
func New() Prompter { return Prompter{} }

// Confirm never asks; it returns interfaces.NonInteractiveError.
func (Prompter) Confirm(context.Context, string) (bool, error) {
	return false, interfaces.NonInteractiveError()
}
