//go:build integration

package integration

import (
	"context"
	"io"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	outboxclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/outbox"
	elasticmqdb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/elasticmq"
	miniodb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/outboxtest"
)

// requireIntegration skips in -short mode, unless INTEGRATION or CI is set (the
// suite gate), and when no container provider is reachable.
func requireIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() || (os.Getenv("INTEGRATION") == "" && os.Getenv("CI") == "") {
		t.Skip("set INTEGRATION=1 or CI=1 (without -short) to run integration tests")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
}

// enqueuedAt returns every enqueued record's creation time: fixed in the past, so
// the record is due at once and its date-partitioned S3 key is deterministic.
func enqueuedAt() time.Time {
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
}

// newOutboxStore starts Postgres and returns the migrated reference store.
func newOutboxStore(t *testing.T) *outboxtest.SQLStore {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { pg.Close(context.Background()) })
	db, err := pg.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := outboxtest.NewSQLStore(db, time.Minute)
	require.NoError(t, store.Migrate(ctx))
	return store
}

// awsConfig is a static-credential AWS config for a local emulator.
func awsConfig(t *testing.T, key, secret string) aws.Config {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(key, secret, ""),
		),
	)
	require.NoError(t, err)
	return cfg
}

// enqueueN enqueues n records on lane for tenant and returns their ids.
func enqueueN(t *testing.T, store *outboxtest.SQLStore, lane string, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range n {
		ids[i] = uuid.New()
		rec := types.OutboxRecord{
			ID: ids[i], Lane: lane, Tenant: "org-1", Key: strconv.Itoa(i),
			Payload: []byte(`{"seq":` + strconv.Itoa(i) + `}`), CreatedAt: enqueuedAt(),
		}
		require.NoError(t, store.Enqueue(context.Background(), &rec))
	}
	return ids
}

// drain runs the relay until the lane has nothing pending (bounded).
func drain(t *testing.T, relay *outbox.Relay, store interfaces.OutboxStore, lane string) {
	t.Helper()
	ctx := context.Background()
	for range 10 {
		require.NoError(t, relay.RunOnce(ctx))
		stats, err := store.Stats(ctx, lane)
		require.NoError(t, err)
		if stats.Pending == 0 {
			return
		}
	}
	t.Fatal("relay did not drain the lane")
}

// TestOutboxStoreConformance_SQLReference runs the OutboxStore conformance suite
// against the reference database/sql store on a real Postgres.
//
// Why this test is important:
//   - The suite is what every consumer store is held to; it must itself pass
//     against a real Postgres (SKIP LOCKED, the DB clock), or it proves nothing.
//
// What it tests:
//   - Disjoint concurrent claims, next_attempt_at honoured, sent/parked rows
//     excluded, and Stats, on the reference store.
func TestOutboxStoreConformance_SQLReference(t *testing.T) {
	requireIntegration(t)
	store := newOutboxStore(t)
	outboxtest.Run(t, outboxtest.Harness{Store: store, Enqueue: store.Enqueue})
}

// TestRelay_EndToEnd_PostgresToSQS drains a Postgres outbox into a real SQS
// (ElasticMQ) queue through the factory-built, decorated sink.
//
// Why this test is important:
//   - Only real infrastructure proves the whole path — claim, batch send, per-row
//     finalize — delivers every row exactly once and empties the lane.
//
// What it tests:
//   - 25 enqueued rows arrive as 25 messages whose bodies are the payloads and
//     whose outbox_id attributes are the row ids; the lane ends with 0 pending.
func TestRelay_EndToEnd_PostgresToSQS(t *testing.T) {
	requireIntegration(t)
	ctx := context.Background()
	store := newOutboxStore(t)
	emq, err := elasticmqdb.NewTestElasticMQ(ctx)
	require.NoError(t, err, "start elasticmq")
	t.Cleanup(func() { emq.Close(context.Background()) })

	api := awssqs.NewFromConfig(
		awsConfig(t, elasticmqdb.AccessKey, elasticmqdb.SecretKey),
		func(o *awssqs.Options) { o.BaseEndpoint = aws.String(emq.Endpoint()) },
	)
	created, err := api.CreateQueue(
		ctx,
		&awssqs.CreateQueueInput{QueueName: aws.String("outbox-e2e")},
	)
	require.NoError(t, err)

	cfg := outboxclient.DefaultConfig()
	cfg.Kind = outboxclient.KindSQS
	cfg.SQS.API, cfg.SQS.Queue = api, "outbox-e2e"
	sink, err := outboxclient.NewFromConfig(cfg, clientdecorators.Deps{})
	require.NoError(t, err)

	ids := enqueueN(t, store, "events", 25)
	relay := outbox.NewRelay(
		outbox.DefaultRelayConfig("events"),
		store,
		sink,
		nil,
		nil,
		nil,
	)
	drain(t, relay, store, "events")

	got := map[string]string{}
	for len(got) < len(ids) {
		out, err := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl: created.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames: []string{"All"},
		})
		require.NoError(t, err)
		require.NotEmpty(t, out.Messages, "queue ran dry after %d messages", len(got))
		for _, m := range out.Messages {
			id := aws.ToString(m.MessageAttributes["outbox_id"].StringValue)
			require.NotContains(t, got, id, "duplicate delivery")
			got[id] = aws.ToString(m.Body)
		}
	}
	for i, id := range ids {
		require.Equal(t, `{"seq":`+strconv.Itoa(i)+`}`, got[id.String()])
	}
	stats, err := store.Stats(ctx, "events")
	require.NoError(t, err)
	require.Equal(t, types.OutboxStats{}, stats)
}

// TestRelay_EndToEnd_PostgresToS3ObjectLock drains a Postgres outbox into an
// Object Lock bucket (MinIO) through the factory-built, decorated sink.
//
// Why this test is important:
//   - An audit lane lands in a WORM bucket: Object Lock rejects a put without an
//     integrity header, so only a real locked bucket proves the sink's
//     SHA-256 checksum and key template work and that objects inherit retention.
//
// What it tests:
//   - Every row becomes one object at {tenant}/{yyyy}/{mm}/{dd}/{id} holding its
//     payload, each under the bucket's default COMPLIANCE retention, and the lane
//     ends with 0 pending.
func TestRelay_EndToEnd_PostgresToS3ObjectLock(t *testing.T) {
	requireIntegration(t)
	ctx := context.Background()
	store := newOutboxStore(t)
	mio, err := miniodb.NewTestMinIO(ctx)
	require.NoError(t, err, "start minio")
	t.Cleanup(func() { mio.Close(context.Background()) })

	api := awss3.NewFromConfig(
		awsConfig(t, miniodb.RootUser, miniodb.RootPassword),
		func(o *awss3.Options) {
			o.BaseEndpoint, o.UsePathStyle = aws.String(mio.Endpoint()), true
		},
	)
	const bucket = "audit-worm"
	_, err = api.CreateBucket(
		ctx,
		&awss3.CreateBucketInput{
			Bucket:                     aws.String(bucket),
			ObjectLockEnabledForBucket: aws.Bool(true),
		},
	)
	require.NoError(t, err)
	_, err = api.PutObjectLockConfiguration(ctx, &awss3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &s3types.ObjectLockConfiguration{
			ObjectLockEnabled: s3types.ObjectLockEnabledEnabled,
			Rule: &s3types.ObjectLockRule{DefaultRetention: &s3types.DefaultRetention{
				Mode: s3types.ObjectLockRetentionModeCompliance, Days: aws.Int32(1),
			}},
		},
	})
	require.NoError(t, err)

	cfg := outboxclient.DefaultConfig()
	cfg.Kind = outboxclient.KindS3
	cfg.S3.API, cfg.S3.Bucket = api, bucket
	sink, err := outboxclient.NewFromConfig(cfg, clientdecorators.Deps{})
	require.NoError(t, err)

	ids := enqueueN(t, store, "audit", 5)
	relay := outbox.NewRelay(
		outbox.DefaultRelayConfig("audit"),
		store,
		sink,
		nil,
		nil,
		nil,
	)
	drain(t, relay, store, "audit")

	listed, err := api.ListObjectsV2(
		ctx,
		&awss3.ListObjectsV2Input{Bucket: aws.String(bucket)},
	)
	require.NoError(t, err)
	keys := make([]string, 0, len(listed.Contents))
	for _, o := range listed.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	day := enqueuedAt().Format("2006/01/02")
	want := make([]string, len(ids))
	for i, id := range ids {
		want[i] = "org-1/" + day + "/" + id.String()
	}
	sort.Strings(keys)
	sort.Strings(want)
	require.Equal(t, want, keys)

	for i, id := range ids {
		key := aws.String("org-1/" + day + "/" + id.String())
		head, err := api.HeadObject(
			ctx,
			&awss3.HeadObjectInput{Bucket: aws.String(bucket), Key: key},
		)
		require.NoError(t, err)
		require.Equal(t, s3types.ObjectLockModeCompliance, head.ObjectLockMode)
		obj, err := api.GetObject(
			ctx,
			&awss3.GetObjectInput{Bucket: aws.String(bucket), Key: key},
		)
		require.NoError(t, err)
		body, err := io.ReadAll(obj.Body)
		_ = obj.Body.Close()
		require.NoError(t, err)
		require.Equal(t, `{"seq":`+strconv.Itoa(i)+`}`, string(body))
	}
	stats, err := store.Stats(ctx, "audit")
	require.NoError(t, err)
	require.Equal(t, types.OutboxStats{}, stats)
}

// TestRelay_EndToEnd_RoutesIsolateQueues runs the routing conformance check over
// the SQS sink against real ElasticMQ queues.
//
// Why this test is important:
//   - One relay drains every document event type to its own queue; only real
//     queues prove a missing queue fails its own rows while every other route
//     is delivered.
//
// What it tests:
//   - standard, large, deletion, facet and notification rows reach their own
//     queues exactly, and ordered rows reach a FIFO queue (so each carries a
//     message group id); member_removed rows, whose queue does not exist, stay
//     pending.
func TestRelay_EndToEnd_RoutesIsolateQueues(t *testing.T) {
	requireIntegration(t)
	ctx := context.Background()
	store := newOutboxStore(t)
	emq, err := elasticmqdb.NewTestElasticMQ(ctx)
	require.NoError(t, err, "start elasticmq")
	t.Cleanup(func() { emq.Close(context.Background()) })
	api := awssqs.NewFromConfig(
		awsConfig(t, elasticmqdb.AccessKey, elasticmqdb.SecretKey),
		func(o *awssqs.Options) { o.BaseEndpoint = aws.String(emq.Endpoint()) },
	)

	healthy := []string{
		"standard",
		"large",
		"deletion",
		"facet",
		"notification",
		"ordered",
	}
	routes := map[string]string{"member_removed": "q-member-removed"} // never created
	urls := map[string]string{}
	for _, route := range healthy {
		in := &awssqs.CreateQueueInput{QueueName: aws.String("q-" + route)}
		if route == "ordered" { // a FIFO queue rejects any entry without a group id
			in.QueueName = aws.String("q-ordered.fifo")
			in.Attributes = map[string]string{"FifoQueue": "true"}
		}
		routes[route] = aws.ToString(in.QueueName)
		out, err := api.CreateQueue(ctx, in)
		require.NoError(t, err)
		urls[route] = aws.ToString(out.QueueUrl)
	}

	cfg := outboxclient.DefaultConfig()
	cfg.Kind = outboxclient.KindSQS
	cfg.SQS.API, cfg.SQS.Routes = api, routes
	sink, err := outboxclient.NewFromConfig(cfg, clientdecorators.Deps{})
	require.NoError(t, err)

	outboxtest.RunRouting(
		t,
		outboxtest.Harness{Store: store, Enqueue: store.Enqueue},
		outboxtest.Routing{
			RunOnce: func(ctx context.Context, lane string) error {
				return outbox.NewRelay(outbox.DefaultRelayConfig(lane), store, sink, nil, nil, nil).
					RunOnce(ctx)
			},
			Healthy: healthy, Broken: "member_removed",
			Delivered: func(ctx context.Context, route string) ([]string, error) {
				var got []string
				for {
					out, err := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
						QueueUrl: aws.String(
							urls[route],
						),
						MaxNumberOfMessages: 10,
						WaitTimeSeconds:     1,
					})
					if err != nil || len(out.Messages) == 0 {
						return got, apperr.Wrap(
							err,
							apperr.CodeUnavailable,
							"receive sqs messages",
						)
					}
					for _, m := range out.Messages {
						got = append(got, aws.ToString(m.Body))
					}
				}
			},
		},
	)
}
