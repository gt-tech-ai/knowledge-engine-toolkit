package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	msgredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// These are the pure-logic unit tests for the Redis messaging backend —
// interface mock-satisfaction, the config-kind bridge, and the nil-client guards.
// The actual PUBLISH/SUBSCRIBE/PSUBSCRIBE behavior can only be exercised against a
// real Redis (the backend wraps a concrete go-redis client with no injectable seam,
// unlike the SQS backend's mockable API), so those round-trips live in the
// integration suite: go/tests/integration/clients_redis_messaging_test.go.

// TestMessagingCapabilityInterfaces_MocksSatisfyContracts tests that the generated
// mocks for the dynamic pub/sub capability interfaces satisfy their contracts.
//
// Why this test is important:
//   - DynamicConsumer and PatternConsumer are core capability interfaces that compose
//     with MessageConsumer (ARCHITECTURE.md#interface-composition); a consumer of the
//     Redis backend depends on them, so their shape and their generated mocks must stay
//     in lockstep.
//
// What it tests:
//   - The mockgen-generated MockDynamicConsumer / MockPatternConsumer are assignable
//     to their core interfaces and construct without panic.
func TestMessagingCapabilityInterfaces_MocksSatisfyContracts(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	var dyn interfaces.DynamicConsumer = mocks.NewMockDynamicConsumer(ctrl)
	var pat interfaces.PatternConsumer = mocks.NewMockPatternConsumer(ctrl)
	require.NotNil(t, dyn)
	require.NotNil(t, pat)
}

// TestMessaging_ParseKind tests the config-kind string → Kind bridge.
//
// Why this test is important:
//   - ParseKind is how a composition root selects a messaging backend from the
//     messaging.kind config value; a wrong mapping silently wires the wrong backend,
//     and a typo must fail loudly rather than default silently.
//
// What it tests:
//   - "sqs"/"memory"/"redis" map to their Kinds; an unknown string errors.
func TestMessaging_ParseKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want messaging.Kind
	}{
		{"sqs", messaging.KindSQS},
		{"memory", messaging.KindMemory},
		{"redis", messaging.KindRedis},
	}
	for _, c := range cases {
		got, err := messaging.ParseKind(c.in)
		require.NoError(t, err, "kind %q", c.in)
		require.Equal(t, c.want, got, "kind %q", c.in)
	}
	_, err := messaging.ParseKind("nope")
	require.Error(t, err)
}

// TestRedisMessaging_NilClient_Rejected tests that the Redis backend constructors
// reject a nil client rather than silently dialing their own connection.
//
// Why this test is important:
//   - The backend must share the cache tier's go-redis client ("no second
//     connection"); a nil client is a wiring bug that must fail loudly at
//     construction, not at first publish.
//
// What it tests:
//   - NewFromConfig / NewPublisher / NewSubscriber with a nil client each return a
//     coded InvalidInput error.
func TestRedisMessaging_NilClient_Rejected(t *testing.T) {
	t.Parallel()
	_, _, fromErr := msgredis.NewFromConfig(msgredis.Config{})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(fromErr))

	_, pubErr := msgredis.NewPublisher(msgredis.Config{})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(pubErr))

	_, subErr := msgredis.NewSubscriber(msgredis.Config{})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(subErr))

	// The parent messaging factory also rejects KindRedis with no injected client
	// (WithRedisClient omitted), rather than dialing or panicking on first publish.
	_, parentPubErr := messaging.NewPublisher(messaging.KindRedis)
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(parentPubErr))
	_, parentSubErr := messaging.NewSubscriber(messaging.KindRedis)
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(parentSubErr))
}
