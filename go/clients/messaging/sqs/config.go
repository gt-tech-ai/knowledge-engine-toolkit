// Package sqs provides an AWS-SDK-backed SQS messaging integration (publisher +
// subscriber) usable against real AWS SQS and against ElasticMQ for local dev.
package sqs

import (
	"context"
	"fmt"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Config holds SQS-specific messaging configuration.
type Config struct {
	// Logger logs subscriber receive/handle/delete errors (the subscriber runs
	// in a goroutine, so its errors have nowhere to return). Nil skips logging.
	Logger interfaces.Logger

	// Ready, when set, gates every receive: the subscriber calls it before each
	// ReceiveMessage and, while it errors, logs, backs off and receives nothing. A
	// consumer whose downstream store is down passes a store probe here, so an
	// outage pauses consumption instead of raising each message's receive count
	// toward the dead-letter queue. Nil receives unconditionally.
	Ready func(ctx context.Context) error

	// API is an optional injected SQS client. When nil, NewPublisher/NewSubscriber
	// build the real aws-sdk-go-v2 client from the region/endpoint/credentials
	// below; a mock injected here exercises the messaging logic without a network.
	API API

	// AWSRegion is the AWS region where the SQS queues are provisioned.
	AWSRegion string

	// QueueURL is the legacy base queue URL retained for backward compatibility
	// with the factory and the Adapter; the real client resolves per-topic queue
	// URLs via ensureQueue (queue name → URL), so it is unused by Publisher/Subscriber.
	QueueURL string

	// Endpoint overrides the SQS service endpoint (e.g. "http://localhost:9324"
	// for ElasticMQ). Empty uses the default AWS endpoint for the region.
	Endpoint string

	// AccessKeyID is the static access key for local development (ElasticMQ).
	// Empty falls back to the default AWS credential chain (env, IAM role / IRSA,
	// shared credentials file).
	AccessKeyID string

	// SecretAccessKey is the static secret key paired with AccessKeyID: required
	// when AccessKeyID is set, ignored when it is empty.
	SecretAccessKey string

	// Handlers runs each handler call through the handler tier
	// (client.NewWireBase with client.Handler); the zero Base runs handlers undecorated.
	Handlers client.Base

	// NackDelay, when Max is positive, hides a message whose handler failed for a
	// uniformly jittered delay in [Min, Max] (ChangeMessageVisibility) instead of
	// leaving it for the queue's visibility timeout. Zero keeps the queue default.
	NackDelay NackDelay

	// BaseBackoff overrides the subscriber's initial receive-retry wait; 0 uses
	// the package default. Tests set a short value so the retry path runs fast.
	BaseBackoff time.Duration

	// WaitTimeSeconds is the subscriber's long-poll wait per receive, 0 to 20
	// (the SQS maximum); 0 uses the package default of 10.
	WaitTimeSeconds int32
}

// NackDelay bounds the jittered visibility delay a failed message gets before it
// redelivers. SQS takes whole seconds up to 12 h, so both bounds are truncated to
// seconds and Max may not exceed 12 h.
type NackDelay struct {
	// Min is the shortest delay; it must be non-negative and at most Max.
	Min time.Duration

	// Max is the longest delay; zero disables the nack delay.
	Max time.Duration
}

// validate rejects poll settings SQS would refuse on every call, so a
// misconfiguration fails construction instead of the receive loop.
func (c *Config) validate() error {
	if c.WaitTimeSeconds < 0 || c.WaitTimeSeconds > maxWaitTimeSeconds {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"sqs: wait time %ds outside [0, %d]", c.WaitTimeSeconds, maxWaitTimeSeconds,
		))
	}
	d := c.NackDelay
	if d.Min < 0 || d.Min > d.Max || d.Max > maxVisibilityTimeout {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"sqs: nack delay [%s, %s] must satisfy 0 <= min <= max <= %s",
			d.Min, d.Max, maxVisibilityTimeout,
		))
	}
	return nil
}
