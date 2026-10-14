package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	outboxstore "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/server"
)

// TestClientFamilies_RejectAnUnknownKind tests every client family's factory against
// a kind it does not ship.
//
// Why this test is important:
//   - The kind is the one config value that picks a backend; a typo must stop the
//     process at startup with an error naming the family, never fall back to another
//     backend or return a nil client that panics on first use.
//
// What it tests:
//   - Each family's NewFromConfig (and the connector builder's Build) with kind
//     "bogus" is CodeInvalidInput, and the message names the family and the bad kind.
func TestClientFamilies_RejectAnUnknownKind(t *testing.T) {
	t.Parallel()
	const bogus client.Kind = "bogus"
	families := map[string]func(ctx context.Context) error{
		"analytics": func(ctx context.Context) error {
			_, err := analytics.NewFromConfig(
				ctx,
				analytics.Config{Kind: bogus},
				analytics.Deps{},
			)
			return err
		},
		"cache": func(ctx context.Context) error {
			_, err := cache.NewFromConfig(ctx, cache.Config{Kind: bogus}, cache.Deps{})
			return err
		},
		"connector": func(ctx context.Context) error {
			_, err := connector.NewSourceBuilder(connector.Config{}, connector.Deps{}).
				Build(ctx, interfaces.SourceConfig{Kind: interfaces.ConnectorKind(bogus)})
			return err
		},
		"cql": func(ctx context.Context) error {
			_, err := cql.NewFromConfig(ctx, cql.Config{Kind: bogus}, cql.Deps{})
			return err
		},
		"database": func(ctx context.Context) error {
			_, err := database.NewFromConfig(
				ctx,
				database.Config{Kind: bogus},
				database.Deps{},
			)
			return err
		},
		"lock": func(ctx context.Context) error {
			_, err := lock.NewFromConfig(ctx, lock.Config{
				Kind: bogus, TTL: time.Minute, RenewInterval: time.Second,
			}, lock.Deps{})
			return err
		},
		"messaging": func(ctx context.Context) error {
			_, err := messaging.NewFromConfig(
				ctx,
				messaging.Config{Kind: bogus},
				messaging.Deps{},
			)
			if err == nil {
				_, err = messaging.NewConsumerFromConfig(
					ctx, messaging.Config{Kind: bogus}, messaging.Deps{},
				)
			}
			return err
		},
		"outbox": func(ctx context.Context) error {
			_, err := outbox.NewFromConfig(ctx, outbox.Config{Kind: bogus}, outbox.Deps{})
			return err
		},
		"outbox store": func(ctx context.Context) error {
			_, err := outboxstore.NewFromConfig(
				ctx, outboxstore.Config{Kind: bogus}, outboxstore.Deps{},
			)
			return err
		},
		"prometheus": func(ctx context.Context) error {
			_, err := prometheus.NewFromConfig(
				ctx, prometheus.Config{Kind: bogus}, prometheus.Deps{},
			)
			return err
		},
		"replaybuffer": func(ctx context.Context) error {
			_, err := replaybuffer.NewFromConfig(
				ctx,
				replaybuffer.Config{Kind: bogus, MaxSize: 1, TTL: time.Minute},
				replaybuffer.Deps{},
			)
			return err
		},
		"rpc": func(ctx context.Context) error {
			_, err := rpc.NewFromConfig(ctx, rpc.Config{Kind: bogus}, rpc.Deps{})
			return err
		},
		"secrets": func(ctx context.Context) error {
			_, err := secrets.NewFromConfig(
				ctx,
				secrets.Config{Kind: bogus},
				secrets.Deps{},
			)
			return err
		},
		"storage": func(ctx context.Context) error {
			_, err := storage.NewFromConfig(
				ctx,
				storage.Config{Kind: bogus},
				storage.Deps{},
			)
			return err
		},
		"transport": func(ctx context.Context) error {
			_, err := server.NewFromConfig(
				ctx, server.Config{Kind: bogus}, server.Deps{},
			)
			return err
		},
	}
	for family, build := range families {
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			err := build(t.Context())
			assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
			assert.ErrorContains(t, err, family+`: unknown kind "bogus"`)
		})
	}
}

// TestKindsBuild_NeverRetriesAnAtMostOnceKind tests Kinds.Build's per-kind retry rule.
//
// Why this test is important:
//   - A retried at-most-once operation (a Redis PUBLISH) is a duplicate delivery; the
//     family table, not an if in each factory, must say which kinds may not retry, and
//     no config may turn retry back on for them.
//
// What it tests:
//   - With client retry on in config, a kind outside AtMostOnce is decorated with a
//     retrier; an AtMostOnce kind, named or selected as the Default, is not.
func TestKindsBuild_NeverRetriesAnAtMostOnceKind(t *testing.T) {
	t.Parallel()
	backend := func(context.Context, struct{}, struct{}) (string, error) { return "", nil }
	var decorated []decorate.Deps
	kinds := client.Kinds[struct{}, struct{}, string]{
		Family:     "toy",
		Default:    "once",
		AtMostOnce: []client.Kind{"once"},
		Backends: map[client.Kind]client.Backend[struct{}, struct{}, string]{
			"any":  backend,
			"once": backend,
		},
		Decorate: func(base, _ string, deps decorate.Deps) string {
			decorated = append(decorated, deps)
			return base
		},
	}
	on := true
	cfg := decoration.DefaultConfig()
	cfg.Clients.Defaults.Retry.Enabled = &on
	decs := decorate.NewDecorations(&cfg, decorate.Collaborators{})

	for _, kind := range []client.Kind{"any", "once", ""} {
		_, err := kinds.Build(t.Context(), kind, struct{}{}, struct{}{}, decs, "")
		require.NoError(t, err)
	}
	require.Len(t, decorated, 3)
	assert.NotNil(t, decorated[0].Retrier)
	assert.Nil(t, decorated[1].Retrier)
	assert.Nil(t, decorated[2].Retrier)
}
