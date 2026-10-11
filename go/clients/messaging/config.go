package messaging

import (
	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Config is the top-level configuration for the messaging package.
// (Field order is fieldalignment-optimized.)
type Config struct {
	// Redis configures the Redis Pub/Sub backend (used when Kind is KindRedis).
	Redis redis.Config
	// Logger decorates the publisher with failure logging and is passed to the
	// subscribers to surface receive/handle errors.
	Logger interfaces.Logger
	// Metrics decorates the publisher and subscribers with counters + latency.
	Metrics interfaces.Metrics
	// Retrier decorates the publisher, retrying a failed publish per its policy.
	Retrier interfaces.Retrier
	// SQS configures the SQS backend (used when Kind is KindSQS).
	SQS sqs.Config
	// Kind selects which backend the factory builds (KindSQS or KindRedis).
	Kind Kind
}

// DefaultConfig returns the default messaging configuration with KindSQS and
// us-east-1 region.
func DefaultConfig() Config {
	return Config{
		Kind: KindSQS,
		SQS: sqs.Config{
			AWSRegion: "us-east-1",
		},
	}
}

// WithAWSRegion sets the AWS region for the SQS backend.
func WithAWSRegion(region string) options.Option[Config] {
	return func(c *Config) {
		c.SQS.AWSRegion = region
	}
}

// WithQueueURL sets the base queue URL for the SQS backend.
func WithQueueURL(url string) options.Option[Config] {
	return func(c *Config) {
		c.SQS.QueueURL = url
	}
}

// WithEndpoint overrides the SQS service endpoint (e.g. ElasticMQ at
// "http://localhost:9324"). Empty uses the default AWS endpoint for the region.
func WithEndpoint(endpoint string) options.Option[Config] {
	return func(c *Config) {
		c.SQS.Endpoint = endpoint
	}
}

// WithStaticCredentials sets static AWS credentials for local development
// (ElasticMQ). Empty keys fall back to the default AWS credential chain.
func WithStaticCredentials(accessKeyID, secretAccessKey string) options.Option[Config] {
	return func(c *Config) {
		c.SQS.AccessKeyID = accessKeyID
		c.SQS.SecretAccessKey = secretAccessKey
	}
}

// WithRedisClient injects the shared go-redis client used by the Redis Pub/Sub
// backend (KindRedis), typically the cache tier's Cache.Client(). The backend never
// dials its own connection, so this is required when Kind is KindRedis.
func WithRedisClient(client *goredis.Client) options.Option[Config] {
	return func(c *Config) {
		c.Redis.Client = client
	}
}

// WithLogger sets the logger that decorates the publisher with failure logging and
// is passed to the SQS/Redis subscribers to report receive/handle errors (their
// goroutines have nowhere else to surface them).
func WithLogger(logger interfaces.Logger) options.Option[Config] {
	return func(c *Config) {
		c.Logger = logger
		c.SQS.Logger = logger
		c.Redis.Logger = logger
	}
}

// WithMetrics sets the metrics registry that decorates the publisher with publish
// counters + latency and the SQS/Redis subscribers with per-message handle metrics.
func WithMetrics(metrics interfaces.Metrics) options.Option[Config] {
	return func(c *Config) {
		c.Metrics = metrics
		c.SQS.Metrics = metrics
		c.Redis.Metrics = metrics
	}
}

// WithRetrier sets the retrier that decorates the publisher, retrying a failed
// publish per the retrier's policy.
func WithRetrier(retrier interfaces.Retrier) options.Option[Config] {
	return func(c *Config) {
		c.Retrier = retrier
	}
}
