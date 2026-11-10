package unit_test

import (
	"context"
	"maps"
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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus/httpapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	outboxstore "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect"
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

	testClientFamiliesRejectAnUnknownKindEachFamily(t)
}

// testClientFamiliesRejectAnUnknownKindEachFamily continues
// testClientFamiliesRejectAnUnknownKind: each family.
func testClientFamiliesRejectAnUnknownKindEachFamily(t *testing.T) {
	t.Helper()

	families := map[string]func(ctx context.Context) error{}

	maps.Copy(families, unknownKindBuilders1())
	maps.Copy(families, unknownKindCoordinationBuilders())
	maps.Copy(families, unknownKindStreamingBuilders())
	maps.Copy(families, unknownKindDeliveryBuilders())
	maps.Copy(families, unknownKindModelBuilders())

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
//   - With client retry on in config, a transient failure on a kind outside AtMostOnce
//     is retried; on an AtMostOnce kind, named or selected as the Default, it is not.
func TestKindsBuild_NeverRetriesAnAtMostOnceKind(t *testing.T) {
	t.Parallel()

	backend := func(context.Context, struct{}, struct{}) (toyClient, error) {
		return toyBackend{}, nil
	}

	var attempts []int

	kinds := client.Kinds[struct{}, struct{}, toyClient]{
		Family:     "toy",
		Default:    textOnce,
		AtMostOnce: []client.Kind{textOnce},
		Backends: map[client.Kind]client.Backend[struct{}, struct{}, toyClient]{
			"any":    backend,
			textOnce: backend,
		},
		Decorate: func(
			base toyClient, decs decorate.Decorations, name string, opts ...client.Option,
		) (toyClient, error) {
			currentBase, err := client.NewBase[toyClient](decs, name, opts...)
			if err != nil {
				return nil, err
			}

			count := 0

			_, _ = currentBase.Exec(
				t.Context(),
				"ping",
				func(context.Context) (struct{}, error) {
					count++

					return struct{}{}, apperr.New(apperr.CodeUnavailable, textDown)
				},
			)

			attempts = append(attempts, count)

			return base, nil
		},
		Options: nil,
	}
	on := true
	cfg := decoration.DefaultConfig()

	cfg.Clients.Defaults.Retry.Enabled = &on
	cfg.Clients.Defaults.Retry.RetryConfig.InitialInterval = time.Millisecond
	cfg.Clients.Defaults.Retry.RetryConfig.MaxInterval = time.Millisecond

	decs := decorate.NewDecorations(&cfg, stdx.Zero[decorate.Collaborators]())

	for _, kind := range []client.Kind{"any", textOnce, ""} {
		_, err := kinds.Build(t.Context(), kind, struct{}{}, struct{}{}, decs, "")
		require.NoError(t, err)
	}

	require.Len(t, attempts, wantAttemptsLen)
	assert.Greater(t, attempts[0], 1)
	assert.Equal(t, 1, attempts[1])
	assert.Equal(t, 1, attempts[2])
}

// toyClient is the contract of the toy family's backends.
type toyClient interface {
	// Ping probes the backend.
	Ping(ctx context.Context) error
}

// toyBackend is a toy backend whose probe always succeeds.
type toyBackend struct{}

// Ping succeeds.
func (toyBackend) Ping(context.Context) error { return nil }

// bogusClientKind is a kind no client family registers.
const bogusClientKind client.Kind = "bogus"

// unknownKindBuilders1 builds one group of client families with the bogus kind.
func unknownKindBuilders1() map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		textAnalytics: func(ctx context.Context) error {
			_, err := analytics.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *analytics.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[analytics.Deps](),
			)

			return err
		},
		"cache": func(ctx context.Context) error {
			_, err := cache.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *cache.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[cache.Deps](),
			)

			return err
		},
		"connector": func(ctx context.Context) error {
			_, err := connector.NewSourceBuilder(stdx.Zero[connector.Config](),
				stdx.Zero[connector.Deps]()).
				Build(ctx, fixtures.BuildRef(func(row *interfaces.SourceConfig) {
					row.Kind = interfaces.ConnectorKind(bogusClientKind)
				}))

			return err
		},
		"cql": func(ctx context.Context) error {
			_, err := cql.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *cql.Config) {
					row.Kind = bogusClientKind
				}),
				cql.Deps{},
			)

			return err
		},
		textDatabase: func(ctx context.Context) error {
			_, err := database.NewFromConfig(
				ctx,
				&database.Config{
					Kind:     bogusClientKind,
					Database: stdx.Zero[infra.DatabaseConfig](),
				},
				database.Deps{},
			)

			return err
		},
	}
}

// unknownKindModelBuilders builds the model-serving client families with the bogus kind.
func unknownKindModelBuilders() map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"llm": func(ctx context.Context) error {
			_, err := llm.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *llm.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[llm.Deps](),
			)

			return err
		},
	}
}

// unknownKindCoordinationBuilders builds one group of client families with the bogus
// kind.
func unknownKindCoordinationBuilders() map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"lock": func(ctx context.Context) error {
			_, err := lock.NewFromConfig(ctx, lock.Config{
				Kind: bogusClientKind, TTL: time.Minute, RenewInterval: time.Second,
				Name: "",
			}, stdx.Zero[lock.Deps]())

			return err
		},
		"messaging": func(ctx context.Context) error {
			_, err := messaging.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *messaging.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[messaging.Deps](),
			)
			if err == nil {
				_, err = messaging.NewConsumerFromConfig(
					ctx,
					fixtures.BuildRef(func(row *messaging.Config) {
						row.Kind = bogusClientKind
					}),
					stdx.Zero[messaging.Deps](),
				)
			}

			return err
		},
		textPrometheus: func(ctx context.Context) error {
			_, err := prometheus.NewFromConfig(
				ctx,
				prometheus.Config{
					Kind: bogusClientKind,
					Name: "",
					HTTP: stdx.Zero[httpapi.Config](),
				},
				stdx.Zero[prometheus.Deps](),
			)

			return err
		},
	}
}

// unknownKindDeliveryBuilders builds the client families that deliver or buffer records
// with the bogus kind.
func unknownKindDeliveryBuilders() map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"outbox": func(ctx context.Context) error {
			_, err := outbox.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *outbox.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[outbox.Deps](),
			)

			return err
		},
		"outbox store": func(ctx context.Context) error {
			_, err := outboxstore.NewFromConfig(
				ctx,
				&outboxstore.Config{
					Kind:     bogusClientKind,
					Name:     "",
					Postgres: stdx.Zero[postgres.Config](),
				},
				stdx.Zero[outboxstore.Deps](),
			)

			return err
		},
		"replaybuffer": func(ctx context.Context) error {
			_, err := replaybuffer.NewFromConfig(
				ctx,
				replaybuffer.Config{
					Kind:      bogusClientKind,
					MaxSize:   1,
					TTL:       time.Minute,
					KeyPrefix: "",
					Name:      "",
				},
				stdx.Zero[replaybuffer.Deps](),
			)

			return err
		},
	}
}

// unknownKindStreamingBuilders builds one group of client families with the bogus kind.
func unknownKindStreamingBuilders() map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"rpc": func(ctx context.Context) error {
			_, err := rpc.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *rpc.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[rpc.Deps](),
			)

			return err
		},
		"secrets": func(ctx context.Context) error {
			_, err := secrets.NewFromConfig(
				ctx,
				fixtures.Build(func(row *secrets.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[secrets.Deps](),
			)

			return err
		},
		"storage": func(ctx context.Context) error {
			_, err := storage.NewFromConfig(
				ctx,
				fixtures.BuildRef(func(row *storage.Config) {
					row.Kind = bogusClientKind
				}),
				stdx.Zero[storage.Deps](),
			)

			return err
		},
		"transport": func(ctx context.Context) error {
			_, err := server.NewFromConfig(
				ctx,
				server.Config{
					Kind:    bogusClientKind,
					Connect: stdx.Zero[connect.ServerConfig](),
				},
				stdx.Zero[server.Deps](),
			)

			return err
		},
	}
}

const (
	// wantAttemptsLen is the expected attempts len.
	wantAttemptsLen = 3
)
