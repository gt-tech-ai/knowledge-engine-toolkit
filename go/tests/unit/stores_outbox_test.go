package unit_test

import (
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	outboxstore "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox"
)

// TestOutboxStoreNewFromConfig_StubBootsAndIsDecorated tests the outbox-store factory.
//
// Why this test is important:
//   - A relay graph must boot with no database (the stub default), a postgres kind
//     wired without its database must fail at boot rather than on the first claim,
//     and the store's statements must be decorated by the stores tier like every
//     other store.
//
// What it tests:
//   - The default kind is an empty outbox: Claim finds nothing, Stats is empty, and
//     DeleteSentBefore removes nothing.
//   - With the stores tier's metrics on, each of those operations is counted once
//     under the store named "outbox".
//   - The postgres kind without a DB is CodeInvalidInput.
func TestOutboxStoreNewFromConfig_StubBootsAndIsDecorated(t *testing.T) {
	t.Parallel()
	reg := promclient.NewRegistry()
	on := true
	cfg := decoration.DefaultConfig()
	cfg.Stores.Defaults.Metrics = &on
	decs := decorate.NewDecorations(&cfg, decorate.Collaborators{
		Observability: decorate.Observability{Metrics: prom.NewFromRegistry(reg)},
	})

	s, err := outboxstore.NewFromConfig(t.Context(), outboxstore.Config{},
		outboxstore.Deps{Decorations: decs})
	require.NoError(t, err)
	recs, err := s.Claim(t.Context(), "events", 10)
	require.NoError(t, err)
	assert.Empty(t, recs)
	stats, err := s.Stats(t.Context(), "events")
	require.NoError(t, err)
	assert.Zero(t, stats.Pending)
	n, err := s.DeleteSentBefore(t.Context(), time.Now())
	require.NoError(t, err)
	assert.Zero(t, n)
	for _, op := range []string{"claim", "stats", "delete_sent_before"} {
		assert.InDelta(t, 1, storeOpCounter(t, reg, op), 0, op)
	}

	_, err = outboxstore.NewFromConfig(t.Context(),
		outboxstore.Config{Kind: outboxstore.KindPostgres}, outboxstore.Deps{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
