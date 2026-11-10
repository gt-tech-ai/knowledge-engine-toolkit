package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
	t.Parallel()

	t.Run("delegates to the wrapped function", func(t *testing.T) {
		t.Parallel()

		want := []string{textA, textB}
		callback := interfaces.DiscovererFunc[string](
			func(_ context.Context, root string) ([]string, error) {
				require.Equalf(t, textRepo, root, "unexpected root %q", root)

				return want, nil
			},
		)

		got, err := callback.Discover(context.Background(), textRepo)
		require.NoError(t, err, textUnexpectedError)
		require.Equalf(t, want, got, "got %v, want %v", got, want)
	})

	t.Run("propagates errors from the wrapped function", func(t *testing.T) {
		t.Parallel()

		sentinel := apperr.Sentinel("discover failed")
		currentCallback := interfaces.DiscovererFunc[string](
			func(_ context.Context, _ string) ([]string, error) {
				return nil, sentinel
			},
		)

		_, err := currentCallback.Discover(context.Background(), textRepo)
		require.ErrorIsf(t, err, sentinel, "expected sentinel error, got %v", err)
	})
}
