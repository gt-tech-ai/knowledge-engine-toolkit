package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestDiscovererFunc_Discover tests that the interfaces.DiscovererFunc adapter forwards
// to its wrapped function and surfaces both results and errors unchanged.
//
// Why this test is important:
//   - interfaces.DiscovererFunc is the func-to-interface bridge that lets a plain closure
//     satisfy Discoverer[T]; if it dropped the result or swallowed the error,
//     jobs would silently iterate over the wrong work units or hide discovery
//     failures
//
// What it tests:
//   - Discover passes the root argument through and returns the wrapped
//     function's slice verbatim
//   - an error returned by the wrapped function is propagated so errors.Is
//     still matches the original sentinel
func TestDiscovererFunc_Discover(t *testing.T) {
	t.Run("delegates to the wrapped function", func(t *testing.T) {
		want := []string{"a", "b"}
		f := interfaces.DiscovererFunc[string](
			func(_ context.Context, root string) ([]string, error) {
				require.Equalf(t, "/repo", root, "unexpected root %q", root)
				return want, nil
			},
		)

		got, err := f.Discover(context.Background(), "/repo")
		require.NoError(t, err, "unexpected error")
		require.Equalf(t, want, got, "got %v, want %v", got, want)
	})

	t.Run("propagates errors from the wrapped function", func(t *testing.T) {
		sentinel := apperr.Sentinel("discover failed")
		f := interfaces.DiscovererFunc[string](
			func(_ context.Context, _ string) ([]string, error) {
				return nil, sentinel
			},
		)

		_, err := f.Discover(context.Background(), "/repo")
		require.ErrorIsf(t, err, sentinel, "expected sentinel error, got %v", err)
	})
}
