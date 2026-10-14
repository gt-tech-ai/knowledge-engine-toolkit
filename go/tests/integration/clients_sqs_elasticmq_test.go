//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/suite"

	messagingclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	elasticmqdb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/elasticmq"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

const emqTestQueue = "test-events"

// SQSElasticMQSuite runs the real AWS-SDK SQS publisher against a real ElasticMQ.
type SQSElasticMQSuite struct {
	testsuite.ElasticMQIntegrationSuite
	raw *awssqs.Client
	cfg infra.SQSConfig
}

// TestSQSElasticMQSuite is the testify entrypoint for the ElasticMQ suite.
//
// Why this test is important:
//   - An outbox relay publishes to SQS in dev via ElasticMQ; only a real
//     ElasticMQ proves the Kind-selected client + endpoint actually deliver.
//
// What it tests:
//   - Wires SQSElasticMQSuite into the runner.
func TestSQSElasticMQSuite(t *testing.T) {
	suite.Run(t, new(SQSElasticMQSuite))
}

func (s *SQSElasticMQSuite) SetupSuite() {
	s.ElasticMQIntegrationSuite.SetupSuite()

	s.cfg = infra.DefaultSQSConfig()
	s.cfg.Endpoint = s.ElasticMQEndpoint

	s.raw = s.rawClient()
	_, err := s.raw.CreateQueue(context.Background(), &awssqs.CreateQueueInput{
		QueueName: aws.String(emqTestQueue),
	})
	s.Require().NoError(err, "create queue")
}

// rawClient builds an SQS client pointed at the test ElasticMQ for queue setup +
// assertions (receive).
func (s *SQSElasticMQSuite) rawClient() *awssqs.Client {
	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				elasticmqdb.AccessKey,
				elasticmqdb.SecretKey,
				"",
			),
		),
	)
	s.Require().NoError(err)
	return awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(s.ElasticMQEndpoint)
	})
}

// TestPublish_RoundTrip verifies a message published through the real publisher is
// received from the queue.
//
// Why this test is important:
//   - This is the wire-level proof the relay's publish path works against the dev
//     broker — queue resolution, send, and delivery.
//
// What it tests:
//   - Publish sends a message that ReceiveMessage reads back with the same body.
func (s *SQSElasticMQSuite) TestPublish_RoundTrip() {
	pub, err := messagingclient.NewFromConfig(
		context.Background(),
		messagingclient.Config{Kind: messagingclient.KindSQS, SQS: s.cfg},
		messagingclient.Deps{},
	)
	s.Require().NoError(err)

	const body = `{"event":"widget.created","id":"abc"}`
	s.Require().NoError(pub.Publish(context.Background(), emqTestQueue, []byte(body)))

	url, err := s.raw.GetQueueUrl(
		context.Background(),
		&awssqs.GetQueueUrlInput{QueueName: aws.String(emqTestQueue)},
	)
	s.Require().NoError(err)
	out, err := s.raw.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
		QueueUrl:            url.QueueUrl,
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     2,
	})
	s.Require().NoError(err)
	s.Require().Len(out.Messages, 1, "published message must be receivable")
	s.JSONEq(body, aws.ToString(out.Messages[0].Body))
}
