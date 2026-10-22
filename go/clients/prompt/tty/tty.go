// Package tty is the interactive Prompter backend: it writes the question to an injected
// writer and reads a y/N answer from an injected reader.
package tty

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Prompter asks on out and answers from in.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

// New returns a Prompter reading answers from in and writing questions to out.
func New(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// Confirm writes "<question> [y/N] " to out and reports whether the next line read
// from in is "y" or "yes" (case-insensitive). Any other line declines. A cancelled ctx
// returns a CodeCanceled error without asking. A reader at EOF before any answer has no
// operator behind it, so Confirm returns interfaces.NonInteractiveError. bufio returns
// io.EOF unwrapped, so it is compared directly.
func (p *Prompter) Confirm(ctx context.Context, question string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, apperr.Wrap(err, apperr.CodeCanceled, "confirmation cancelled")
	}
	if _, err := fmt.Fprintf(p.out, "%s [y/N] ", question); err != nil {
		return false, apperr.Wrap(err, apperr.CodeInternal, "write confirmation prompt")
	}
	line, err := p.in.ReadString('\n')
	switch {
	case err == io.EOF && line == "":
		return false, interfaces.NonInteractiveError()
	case err != nil && err != io.EOF:
		return false, apperr.Wrap(err, apperr.CodeInternal, "read confirmation answer")
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
