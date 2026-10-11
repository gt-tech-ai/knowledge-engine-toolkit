package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// API is the subset of the AWS SQS client used by the publisher and subscriber.
// *awssqs.Client satisfies it; it is an injectable seam (Config.API) so a caller —
// or a black-box test with a generated mock — can supply its own client and
// exercise the messaging logic without a network or a container.
//
// SDK seam — mocks the AWS SQS SDK client (aws-sdk-go-v2/service/sqs), cannot compose
// with a core port.
type API interface {
	// GetQueueUrl resolves an existing queue's URL by name.
	GetQueueUrl(
		ctx context.Context,
		in *awssqs.GetQueueUrlInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.GetQueueUrlOutput, error)

	// CreateQueue creates a queue by name and returns its URL (idempotent).
	CreateQueue(
		ctx context.Context,
		in *awssqs.CreateQueueInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.CreateQueueOutput, error)

	// SendMessage enqueues a single message onto a queue.
	SendMessage(
		ctx context.Context,
		in *awssqs.SendMessageInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.SendMessageOutput, error)

	// ReceiveMessage long-polls a queue for a batch of messages.
	ReceiveMessage(
		ctx context.Context,
		in *awssqs.ReceiveMessageInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.ReceiveMessageOutput, error)

	// DeleteMessage removes a processed message by its receipt handle.
	DeleteMessage(
		ctx context.Context,
		in *awssqs.DeleteMessageInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.DeleteMessageOutput, error)

	// SendMessageBatch enqueues up to 10 messages onto a queue in one request,
	// returning per-entry success/failure so a partial failure is surfaced.
	SendMessageBatch(
		ctx context.Context,
		in *awssqs.SendMessageBatchInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.SendMessageBatchOutput, error)

	// DeleteMessageBatch removes up to 10 processed messages by receipt handle in
	// one request (the ack for a fanned-out receive batch).
	DeleteMessageBatch(
		ctx context.Context,
		in *awssqs.DeleteMessageBatchInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.DeleteMessageBatchOutput, error)

	// ChangeMessageVisibility resets how long a received message stays hidden (the
	// subscriber's jittered nack delay).
	ChangeMessageVisibility(
		ctx context.Context,
		in *awssqs.ChangeMessageVisibilityInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.ChangeMessageVisibilityOutput, error)
}

// NewAWSClient builds the aws-sdk-go-v2 SQS client the publisher and subscriber use,
// for a caller that sends through the SDK directly (an outbox relay). A non-empty
// Endpoint overrides the service endpoint (ElasticMQ); non-empty static credentials
// are used for local dev, otherwise the default AWS credential chain (env, IAM role /
// IRSA, shared file) applies. An AccessKeyID without a SecretAccessKey is
// CodeInvalidInput. Only cfg's region, endpoint and credentials are read; the client
// does no I/O until its first call.
func NewAWSClient(ctx context.Context, cfg Config) (*awssqs.Client, error) {
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"sqs: a static access key needs its secret access key",
		)
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg.AWSRegion != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.AWSRegion))
	}
	if cfg.AccessKeyID != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID,
				cfg.SecretAccessKey,
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "load aws config for sqs")
	}

	return awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}), nil
}

// resolveAPI returns the injected Config.API when set, otherwise builds the real
// aws-sdk-go-v2 SQS client from cfg, loading the AWS config under ctx. It is the
// single seam through which both the
// publisher and subscriber obtain their SQS client (real in production, a mock in
// black-box tests).
func resolveAPI(ctx context.Context, cfg Config) (API, error) {
	if cfg.API != nil {
		return cfg.API, nil
	}
	return NewAWSClient(ctx, cfg)
}

// ensureQueue resolves the URL for queueName, creating the queue only if it does
// not exist. It prefers GetQueueUrl — so a least-privilege production role
// without CreateQueue can still resolve an existing queue — and CreateQueues
// solely on a not-found error. It is idempotent and safe to call from both the
// publisher and the subscriber (whichever boots first creates the queue).
func ensureQueue(ctx context.Context, api API, queueName string) (string, error) {
	out, err := api.GetQueueUrl(
		ctx,
		&awssqs.GetQueueUrlInput{QueueName: aws.String(queueName)},
	)
	if err == nil {
		return aws.ToString(out.QueueUrl), nil
	}

	var notExist *sqstypes.QueueDoesNotExist
	if !apperr.As(err, &notExist) {
		return "", apperr.Wrap(err, apperr.CodeUnavailable, "sqs get queue url")
	}

	created, cerr := api.CreateQueue(
		ctx,
		&awssqs.CreateQueueInput{QueueName: aws.String(queueName)},
	)
	if cerr != nil {
		return "", apperr.Wrap(cerr, apperr.CodeUnavailable, "sqs create queue")
	}
	return aws.ToString(created.QueueUrl), nil
}
