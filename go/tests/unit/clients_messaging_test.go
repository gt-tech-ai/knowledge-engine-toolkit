package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// badAWSConfig points AWS_CA_BUNDLE at a nonexistent file so that the SDK's
// LoadDefaultConfig — and thus the publisher/subscriber construction that has no
// injected API — fails reading it. It uses t.Setenv, so the caller must not be
// parallel.
func badAWSConfig(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_CA_BUNDLE", filepath.Join(t.TempDir(), "missing-ca.pem"))
}

// TestNewPublisher_FailsOnUnloadableAWSConfig tests that NewPublisher surfaces an
// error when the AWS SDK cannot load its configuration (and no API is injected).
//
// Why this test is important:
//   - A construction failure must be reported, not swallowed into a half-built
//     publisher that fails opaquely on first publish
//
// What it tests:
//   - With a malformed AWS config and no injected client, NewPublisher errors
func TestNewPublisher_FailsOnUnloadableAWSConfig(t *testing.T) {
	badAWSConfig(t)

	_, err := messaging.NewPublisher(messaging.KindSQS)
	require.Error(t, err, "expected construction error on unloadable AWS config")
}

// TestNewSubscriber_FailsOnUnloadableAWSConfig tests that NewSubscriber surfaces an
// error when the AWS SDK cannot load its configuration (and no API is injected).
//
// Why this test is important:
//   - A worker that started with a half-built subscriber would silently consume
//     nothing; the construction error must surface at startup
//
// What it tests:
//   - With a malformed AWS config and no injected client, NewSubscriber errors
func TestNewSubscriber_FailsOnUnloadableAWSConfig(t *testing.T) {
	badAWSConfig(t)

	_, err := messaging.NewSubscriber(messaging.KindSQS)
	require.Error(t, err, "expected construction error on unloadable AWS config")
}

// TestMessaging_AllOptions tests that every publisher option is accepted and wired
// by the factory: endpoint + static credentials configure the SQS client, and
// logger/metrics/retrier compose the cross-cutting decorators around it.
//
// Why this test is important:
//   - The factory is the single composition point for the messaging client; an
//     option that was silently dropped would leave a service unobservable or
//     mis-credentialed with no failing test
//
// What it tests:
//   - NewPublisher with the full option set (region, endpoint, credentials, logger,
//     metrics, retrier) returns a non-nil publisher without error
func TestMessaging_AllOptions(t *testing.T) {
	t.Parallel()

	pub, err := messaging.NewPublisher(
		messaging.KindSQS,
		messaging.WithAWSRegion("us-east-1"),
		messaging.WithEndpoint("http://localhost:9324"),
		messaging.WithStaticCredentials("local", "local"),
		messaging.WithLogger(fixtures.NopLogger()),
		messaging.WithMetrics(fixtures.NopMetrics()),
		messaging.WithRetrier(mocks.NewMockRetrier(gomock.NewController(t))),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, pub, "expected non-nil publisher")
}

// Real publisher Publish/PublishBatch behaviour (queue-name → URL resolution,
// SendMessage, create-on-missing) is covered without a network by the mock-API
// tests in go/tests/unit/clients_sqs_messaging_test.go, and
// end-to-end against ElasticMQ by the integration suite. The former stub test
// (which asserted Publish was a no-op returning nil) was removed when the real
// SQS publisher replaced the stub.

// TestWatermillPublisher_InterfaceCompliance tests that the publisher factory
// returns the interfaces.MessagePublisher abstraction.
//
// Why this test is important:
//   - Callers that import the concrete type instead of the interface cannot be
//     swapped to the real SQS backend without a cascade of changes
//
// What it tests:
//   - NewPublisher returns a value assignable to interfaces.MessagePublisher
//   - The returned publisher is non-nil
func TestWatermillPublisher_InterfaceCompliance(t *testing.T) {
	t.Parallel()

	var pub interfaces.MessagePublisher
	var err error
	pub, err = messaging.NewPublisher(
		messaging.KindSQS,
		messaging.WithQueueURL(
			"https://sqs.us-east-1.amazonaws.com/123456789012/test-queue",
		),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, pub, "expected non-nil MessagePublisher")
}

// Real subscriber receive→handle→delete behaviour (long-poll, ack/nack, backoff
// retry, drain-on-close) is covered without a network by the mock-API
// tests in go/tests/unit/clients_sqs_messaging_test.go, and
// end-to-end against ElasticMQ by the integration suite. The former stub test
// (which asserted Subscribe merely stored a handler) was removed when the real
// long-poll subscriber replaced the stub — calling Subscribe now starts a live
// receive loop that would reach AWS, which a unit test must not do.

// TestWatermillSubscriber_InterfaceCompliance tests that the subscriber factory
// returns the interfaces.MessageConsumer abstraction.
//
// Why this test is important:
//   - Callers that import the concrete type instead of the interface cannot be
//     swapped to the real SQS backend without a cascade of changes
//
// What it tests:
//   - NewSubscriber returns a value assignable to interfaces.MessageConsumer
//   - The returned subscriber is non-nil
func TestWatermillSubscriber_InterfaceCompliance(t *testing.T) {
	t.Parallel()

	var sub interfaces.MessageConsumer
	var err error
	sub, err = messaging.NewSubscriber(
		messaging.KindSQS,
		messaging.WithQueueURL(
			"https://sqs.us-east-1.amazonaws.com/123456789012/test-queue",
		),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, sub, "expected non-nil MessageConsumer")
}

// TestSQSAdapter_QueueURL tests that the SQS adapter constructs correct queue URLs from a
// base URL and topic name.
//
// Why this test is important:
//   - Incorrect queue URL construction routes messages to the wrong SQS queue
//   - The URL format must match AWS SQS conventions (base URL + "/" + topic)
//   - Misrouted messages cause silent data loss in event-driven architectures
//
// What it tests:
//   - NewAdapter returns a non-nil adapter
//   - QueueURL concatenates the base URL and topic with a "/" separator
func TestSQSAdapter_QueueURL(t *testing.T) {
	t.Parallel()

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789012"

	adapter, err := messaging.NewAdapter(
		messaging.KindSQS,
		messaging.WithQueueURL(queueURL),
	)
	require.NoError(t, err, "unexpected error creating adapter")
	require.NotNil(t, adapter, "expected non-nil SQS adapter")

	// Verify QueueURL constructs correct path
	expected := queueURL + "/test-topic"
	actual := adapter.QueueURL("test-topic")
	assert.Equal(t, expected, actual)
}

// ---------------------------------------------------------------------------
// Messaging Kind.String()
// ---------------------------------------------------------------------------

// TestMessagingKind_String_SQS tests that the SQS messaging kind produces the
// expected string representation.
//
// Why this test is important:
//   - Kind.String() appears in error messages and metrics labels; an incorrect
//     value makes backend-type attribution in dashboards wrong
//
// What it tests:
//   - KindSQS.String() returns "sqs"
func TestMessagingKind_String_SQS(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "sqs", messaging.KindSQS.String())
}

// TestMessagingKind_String_Unknown tests that an unrecognized messaging kind
// produces a descriptive fallback string rather than panicking or returning empty.
//
// Why this test is important:
//   - Unknown kinds must produce a human-readable sentinel instead of empty;
//     empty strings in logs make debugging impossible
//
// What it tests:
//   - Kind(99).String() returns "Kind(99)"
func TestMessagingKind_String_Unknown(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Kind(99)", messaging.Kind(99).String())
}

// ---------------------------------------------------------------------------
// Messaging WithAWSRegion option
// ---------------------------------------------------------------------------

// TestMessaging_WithAWSRegion tests that the WithAWSRegion option is accepted
// during publisher creation without error.
//
// Why this test is important:
//   - SQS queues are region-specific; incorrect region configuration silently
//     routes messages to a non-existent queue or the wrong account
//
// What it tests:
//   - NewPublisher with WithAWSRegion returns a non-nil publisher with no error
func TestMessaging_WithAWSRegion(t *testing.T) {
	t.Parallel()

	// WithAWSRegion is applied during NewPublisher; verify it doesn't error.
	pub, err := messaging.NewPublisher(
		messaging.KindSQS,
		messaging.WithAWSRegion("eu-west-1"),
		messaging.WithQueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/queue"),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, pub, "expected non-nil publisher")
}

// ---------------------------------------------------------------------------
// Messaging DefaultConfig
// ---------------------------------------------------------------------------

// TestMessaging_DefaultConfig tests that creating a publisher with only the
// required queue URL uses sensible defaults without error.
//
// Why this test is important:
//   - Services that omit explicit region/kind configuration must fall back to
//     safe defaults (KindSQS, us-east-1); wrong defaults silently misroute messages
//
// What it tests:
//   - NewPublisher with only WithQueueURL returns a non-nil publisher with no error
func TestMessaging_DefaultConfig(t *testing.T) {
	t.Parallel()

	// NewPublisher with no options uses DefaultConfig (KindSQS, us-east-1).
	pub, err := messaging.NewPublisher(
		messaging.KindSQS,
		messaging.WithQueueURL("https://sqs.us-east-1.amazonaws.com/123456789012/queue"),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, pub, "expected non-nil publisher")
}

// ---------------------------------------------------------------------------
// Messaging NewAdapter with WithAWSRegion
// ---------------------------------------------------------------------------

// TestSQSAdapter_WithAWSRegion tests that the SQS adapter accepts the
// WithAWSRegion option during creation without error.
//
// Why this test is important:
//   - Non-default region configuration must be wired through without error;
//     silently ignoring the region option would route messages to the wrong region
//
// What it tests:
//   - NewAdapter with WithAWSRegion returns a non-nil adapter with no error
func TestSQSAdapter_WithAWSRegion(t *testing.T) {
	t.Parallel()

	adapter, err := messaging.NewAdapter(
		messaging.KindSQS,
		messaging.WithAWSRegion("ap-southeast-1"),
		messaging.WithQueueURL("https://sqs.ap-southeast-1.amazonaws.com/123456789012"),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, adapter, "expected non-nil adapter")
}

// TestNewPublisher_UnknownKind tests that NewPublisher returns an error for an
// unrecognized messaging kind.
//
// Why this test is important:
//   - A factory that silently falls back to a no-op for unknown kinds would
//     allow misconfigured services to start without publishing any messages
//
// What it tests:
//   - NewPublisher(Kind(99)) returns a non-nil error
func TestNewPublisher_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := messaging.NewPublisher(messaging.Kind(99))
	require.Error(t, err, "expected error for unknown kind")
}

// TestNewSubscriber_UnknownKind tests that NewSubscriber returns an error for
// an unrecognized messaging kind.
//
// Why this test is important:
//   - A factory that silently falls back to a no-op for unknown kinds would
//     allow misconfigured workers to start without consuming any messages
//
// What it tests:
//   - NewSubscriber(Kind(99)) returns a non-nil error
func TestNewSubscriber_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := messaging.NewSubscriber(messaging.Kind(99))
	require.Error(t, err, "expected error for unknown kind")
}

// TestNewAdapter_UnknownKind tests that NewAdapter returns an error for an
// unrecognized messaging kind.
//
// Why this test is important:
//   - An adapter built from an unknown kind would produce incorrect queue URLs
//     and silently route messages to the wrong endpoint
//
// What it tests:
//   - NewAdapter(Kind(99)) returns a non-nil error
func TestNewAdapter_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := messaging.NewAdapter(messaging.Kind(99))
	require.Error(t, err, "expected error for unknown kind")
}
