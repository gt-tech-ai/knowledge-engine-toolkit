package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	pkgmocks "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestReplayBufferFactory tests that NewFromConfig selects the backend by Kind, validates
// its config, and fails loudly on an unknown kind or a redis kind with no client.
//
// Why this test is important:
//   - Backend selection is a config-driven seam (ARCHITECTURE.md#the-one-idea): a
//     mis-selection or a silently accepted bad config (redis without a client,
//     non-positive bound) would surface as a nil-deref or an unbounded buffer at runtime
//     instead of a loud startup error.
//
// What it tests:
//   - KindMemory yields a working buffer; KindRedis without a client errors; an unknown
//     kind errors; a non-positive max_size errors.
func TestReplayBufferFactory(t *testing.T) {
	t.Parallel()

	buf, err := replaybuffer.NewFromConfig(t.Context(), replaybuffer.Config{
		Kind: replaybuffer.KindMemory, MaxSize: 10, TTL: time.Minute,
	}, replaybuffer.Deps{})
	require.NoError(t, err)
	require.NoError(t, buf.Append(context.Background(), "k", "msg-1", []byte("x")))
	tail, complete, err := buf.ReplayAfter(context.Background(), "k", "msg-1")
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Empty(t, tail)

	_, err = replaybuffer.NewFromConfig(t.Context(), replaybuffer.Config{
		Kind: replaybuffer.KindRedis, MaxSize: 10, TTL: time.Minute,
	}, replaybuffer.Deps{})
	require.Error(t, err, "redis kind without a client must fail loudly")

	_, err = replaybuffer.NewFromConfig(t.Context(), replaybuffer.Config{
		Kind: "bogus", MaxSize: 10, TTL: time.Minute,
	}, replaybuffer.Deps{})
	require.Error(t, err, "unknown kind must fail loudly")

	_, err = replaybuffer.NewFromConfig(t.Context(), replaybuffer.Config{
		Kind: replaybuffer.KindMemory, MaxSize: 0, TTL: time.Minute,
	}, replaybuffer.Deps{})
	assert.Error(t, err, "non-positive max_size must fail loudly")
}

// TestReplayBufferTimeoutDecorator tests that the timeout decorator bounds a wedged
// backend so a slow Redis cannot stall the caller (the reconnect-replay hot path).
//
// Why this test is important:
//   - Append runs on the connection's single write pump; without a per-op deadline a
//     wedged Redis would block every subsequent send on that socket. The timeout
//     decorator is the isolation that keeps a slow backend off the send path — this test
//     pins that it actually cancels a stuck op.
//
// What it tests:
//   - wrapping a backend whose Append blocks until its context is cancelled with a 20ms
//     timeout, Append returns a CodeTimeout error well within a second (not blocking
//     forever).
func TestReplayBufferTimeoutDecorator(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := pkgmocks.NewMockReplayBuffer(ctrl)
	base.EXPECT().
		Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _, _ string, _ []byte) error {
			<-ctx.Done() // wedged backend: only returns once the timeout cancels the context
			return ctx.Err()
		})

	buf := replaybuffer.Decorate(
		base,
		"test",
		decorate.Deps{Timeout: 20 * time.Millisecond},
	)

	done := make(chan error, 1)
	go func() { done <- buf.Append(context.Background(), "k", "msg-1", []byte("x")) }()

	select {
	case err := <-done:
		require.Error(t, err, "a wedged Append must be cancelled by the timeout")
		assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
	case <-time.After(time.Second):
		t.Fatal("Append was not bounded by the timeout decorator")
	}
}
