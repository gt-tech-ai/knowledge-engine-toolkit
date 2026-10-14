package unit_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// TestMessaging_SQSConstructionFailsOnUnloadableAWSConfig tests that the SQS kind
// surfaces an AWS config load failure at construction.
//
// Why this test is important:
//   - A half-built publisher fails opaquely on its first publish, and a half-built
//     consumer silently consumes nothing; the failure must surface at startup.
//
// What it tests:
//   - With AWS_CA_BUNDLE pointing at a missing file and no injected API, both
//     NewFromConfig and NewConsumerFromConfig return an error.
func TestMessaging_SQSConstructionFailsOnUnloadableAWSConfig(t *testing.T) {
	t.Setenv("AWS_CA_BUNDLE", filepath.Join(t.TempDir(), "missing-ca.pem"))
	cfg := messaging.Config{Kind: messaging.KindSQS}

	_, err := messaging.NewFromConfig(t.Context(), cfg, messaging.Deps{})
	require.Error(t, err)
	_, err = messaging.NewConsumerFromConfig(t.Context(), cfg, messaging.Deps{})
	require.Error(t, err)
}

// TestMessaging_FactoryBuildsEachKind tests the publisher and consumer tables.
//
// Why this test is important:
//   - The kind is the only thing a deployment changes to move from in-process
//     delivery to SQS or Redis; each kind must build both halves from config alone,
//     and a backend missing its runtime client must fail at startup.
//
// What it tests:
//   - The default (memory) publisher and consumer sharing a Broker deliver a
//     published payload, through the client tier.
//   - The SQS kind with an endpoint and static credentials builds both halves
//     without network I/O.
//   - The redis kind with no injected client is CodeInvalidInput for both halves.
func TestMessaging_FactoryBuildsEachKind(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	deps := messaging.Deps{Broker: memory.NewBroker()}
	pub, err := messaging.NewFromConfig(ctx, messaging.Config{}, deps)
	require.NoError(t, err)
	sub, err := messaging.NewConsumerFromConfig(ctx, messaging.Config{}, deps)
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, "t", []byte("hello")))
	got := make(chan []byte, 1)
	go func() {
		_ = sub.Subscribe(ctx, "t", func(_ context.Context, m *interfaces.Message) error {
			got <- m.Payload
			return nil
		})
	}()
	select {
	case p := <-got:
		assert.Equal(t, []byte("hello"), p)
	case <-ctx.Done():
		t.Fatal("the memory consumer never received the published payload")
	}

	sqsCfg := messaging.Config{Kind: messaging.KindSQS, SQS: infra.SQSConfig{
		Region:          "us-east-1",
		Endpoint:        "http://localhost:9324",
		AccessKeyID:     "local",
		SecretAccessKey: "local",
	}}
	sqsPub, err := messaging.NewFromConfig(ctx, sqsCfg, messaging.Deps{})
	require.NoError(t, err)
	assert.NotNil(t, sqsPub)
	sqsSub, err := messaging.NewConsumerFromConfig(ctx, sqsCfg, messaging.Deps{})
	require.NoError(t, err)
	assert.NotNil(t, sqsSub)

	redisCfg := messaging.Config{Kind: messaging.KindRedis}
	_, err = messaging.NewFromConfig(ctx, redisCfg, messaging.Deps{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = messaging.NewConsumerFromConfig(ctx, redisCfg, messaging.Deps{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
