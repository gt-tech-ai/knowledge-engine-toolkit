package unit_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prompt"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// requireNonInteractive asserts err is the Prompter's coded "pass --yes" error.
func requireNonInteractive(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ae *apperr.AppError
	require.True(t, apperr.As(err, &ae), "want *errors.AppError, got %T", err)
	assert.Equal(t, apperr.CodeInvalidInput, ae.Code)
	assert.Contains(t, err.Error(), "--yes")
}

// TestPrompt_TTYReadsInjectedReader tests that the tty Prompter asks on the injected
// writer and answers from the injected reader.
//
// Why this test is important:
//   - The installer's confirmation must work from any front-end's streams, not only
//     process stdio; a misparsed answer installs (or skips) tools the operator did not
//     choose.
//
// What it tests:
//   - "y" and "yes" confirm; "n" and an empty line decline.
//   - The question followed by " [y/N] " is written to the injected writer.
func TestPrompt_TTYReadsInjectedReader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"\n", false},
	}
	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.in), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			p, err := prompt.NewFromConfig(context.Background(),
				prompt.Config{Kind: prompt.KindTTY},
				prompt.Deps{In: strings.NewReader(tc.in), Out: &out})
			require.NoError(t, err)
			got, err := p.Confirm(context.Background(), "Install pkgsite?")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, "Install pkgsite? [y/N] ", out.String())
		})
	}
}

// TestPrompt_TTYEOFReturnsCodedError tests that the tty Prompter fails loudly when its
// reader has no answer.
//
// Why this test is important:
//   - A non-interactive run (CI, a closed stdin) must not read as "no" and silently
//     install nothing; it must tell the operator to pass --yes.
//
// What it tests:
//   - An empty reader yields a CodeInvalidInput error naming --yes.
func TestPrompt_TTYEOFReturnsCodedError(t *testing.T) {
	t.Parallel()
	p, err := prompt.NewFromConfig(context.Background(),
		prompt.Config{Kind: prompt.KindTTY},
		prompt.Deps{In: strings.NewReader(""), Out: io.Discard})
	require.NoError(t, err)
	_, err = p.Confirm(context.Background(), "Install pkgsite?")
	requireNonInteractive(t, err)
}

// TestPrompt_TTYCancelledContextDoesNotAsk tests that the tty Prompter honours a
// cancelled context instead of reading an answer.
//
// Why this test is important:
//   - A cancelled run (Ctrl-C, a deadline) must not block on, or act on, an answer it
//     reads after the operator gave up.
//
// What it tests:
//   - With a cancelled context and a "y" waiting on the reader, Confirm returns false
//     and a CodeCanceled error, and writes no question.
func TestPrompt_TTYCancelledContextDoesNotAsk(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	p, err := prompt.NewFromConfig(context.Background(),
		prompt.Config{Kind: prompt.KindTTY},
		prompt.Deps{In: strings.NewReader("y\n"), Out: &out})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, err := p.Confirm(ctx, "Install pkgsite?")

	assert.False(t, ok)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeCanceled), "got %v", err)
	assert.Empty(t, out.String())
}

// TestPrompt_DenyReturnsCodedError tests that the deny Prompter refuses every question.
//
// Why this test is important:
//   - deny is the default for a front-end with no operator (an MCP server, a web app);
//     it must fail with the --yes guidance instead of blocking on input it can't read.
//
// What it tests:
//   - Confirm returns a CodeInvalidInput error naming --yes, without reading any input.
func TestPrompt_DenyReturnsCodedError(t *testing.T) {
	t.Parallel()
	p, err := prompt.NewFromConfig(
		context.Background(),
		prompt.Config{Kind: prompt.KindDeny},
		prompt.Deps{},
	)
	require.NoError(t, err)
	_, err = p.Confirm(context.Background(), "Install pkgsite?")
	requireNonInteractive(t, err)
}

// TestPrompt_DefaultIsDeny tests that an empty kind selects the deny prompter.
//
// Why this test is important:
//   - The factory's zero value must do no I/O: a front-end that configures nothing must
//     refuse rather than block on a stdin no one is watching.
//
// What it tests:
//   - An empty Config builds a Prompter whose Confirm is the --yes error.
func TestPrompt_DefaultIsDeny(t *testing.T) {
	t.Parallel()
	p, err := prompt.NewFromConfig(context.Background(), prompt.Config{}, prompt.Deps{})
	require.NoError(t, err)
	_, err = p.Confirm(context.Background(), "Install pkgsite?")
	requireNonInteractive(t, err)
}

// TestPrompt_UnknownKindFails tests that the factory rejects a kind it doesn't know,
// and tty without its streams.
//
// Why this test is important:
//   - Factories fail loudly on an unknown kind rather than falling back to a backend the
//     caller didn't ask for; a tty with no streams would panic on first use.
//
// What it tests:
//   - Kind "bogus" yields a nil Prompter and a CodeInvalidInput error naming deny and
//     tty; tty without In/Out is CodeInvalidInput.
func TestPrompt_UnknownKindFails(t *testing.T) {
	t.Parallel()
	p, err := prompt.NewFromConfig(
		context.Background(),
		prompt.Config{Kind: "bogus"},
		prompt.Deps{},
	)
	assert.Nil(t, p)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "deny")
	require.ErrorContains(t, err, "tty")

	_, err = prompt.NewFromConfig(
		context.Background(),
		prompt.Config{Kind: prompt.KindTTY},
		prompt.Deps{},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
