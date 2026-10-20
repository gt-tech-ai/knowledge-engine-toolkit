package unit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
)

// TestCodebaseSnapshot_MemoComputesOncePerKey tests that a snapshot computes each key's
// value once however many goroutines ask for it.
//
// Why this test is important:
//   - Checks run in parallel over one snapshot; the point of the snapshot is that the
//     second check to need a parsed file, a file list or a rendered chart reuses the
//     first one's work instead of repeating it.
//
// What it tests:
//   - A hundred concurrent Memo calls on one key run build once and all see its value;
//     a second key builds separately; Key returns the snapshot's key.
func TestCodebaseSnapshot_MemoComputesOncePerKey(t *testing.T) {
	t.Parallel()
	s := codebase.NewSnapshot("run-1")
	assert.Equal(t, "run-1", s.Key())
	var calls atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 100)
	for i := range errs {
		wg.Go(func() {
			got, err := s.Memo(t.Context(), "k", func(context.Context) (int, error) {
				calls.Add(1)
				return 42, nil
			})
			errs[i] = err
			assert.Equal(t, 42, got)
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls.Load())
	other, err := s.Memo(t.Context(), "other", func(context.Context) (string, error) {
		return "x", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "x", other)
}

// TestCodebaseSnapshot_MemoErrors tests what Memo caches when a build fails.
//
// Why this test is important:
//   - A failing build (a file that does not parse) must fail every caller the same way
//     without re-running; a cancelled caller must not poison the key for the callers
//     that come after it; a key reused with another type is a programming error that
//     must surface, not a silent zero value.
//
// What it tests:
//   - A build error is returned to later callers without a second build; a context
//     cancellation is not cached, so the next caller builds; a type mismatch on a
//     reused key is CodeInternal.
func TestCodebaseSnapshot_MemoErrors(t *testing.T) {
	t.Parallel()
	s := codebase.NewSnapshot("run-2")
	var calls atomic.Int32
	fail := func(context.Context) (int, error) {
		calls.Add(1)
		return 0, apperr.New(apperr.CodeInvalidInput, "bad file")
	}
	_, err := s.Memo(t.Context(), "bad", fail)
	require.Error(t, err)
	_, err = s.Memo(t.Context(), "bad", fail)
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.Memo(ctx, "cancel", func(ctx context.Context) (int, error) {
		return 0, ctx.Err()
	})
	require.Error(t, err)
	got, err := s.Memo(t.Context(), "cancel", func(context.Context) (int, error) {
		return 7, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 7, got)

	_, err = s.Memo(t.Context(), "cancel", func(context.Context) (string, error) {
		return "", nil
	})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestCodebaseSnapshot_MemoPanickingBuild tests a build that panics.
//
// Why this test is important:
//   - A panicking build that never closed its entry would block every later caller of
//     the key for the snapshot's life; one that crashed the process would take the whole
//     run down with it.
//
// What it tests:
//   - The builder's caller and a caller waiting on the same key both get CodeInternal;
//     the key is not cached, so a later call builds again and gets its value.
func TestCodebaseSnapshot_MemoPanickingBuild(t *testing.T) {
	t.Parallel()
	s := codebase.NewSnapshot("run-3")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	boom := func(context.Context) (int, error) {
		once.Do(func() { close(started) })
		<-release
		panic("parser bug")
	}
	errs := make(chan error, 2)
	go func() {
		_, err := s.Memo(t.Context(), "k", boom)
		errs <- err
	}()
	<-started
	go func() {
		_, err := s.Memo(t.Context(), "k", boom)
		errs <- err
	}()
	close(release)
	for range 2 {
		assert.Equal(t, apperr.CodeInternal, apperr.Code(<-errs))
	}

	got, err := s.Memo(t.Context(), "k", func(context.Context) (int, error) {
		return 3, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, got)
}
