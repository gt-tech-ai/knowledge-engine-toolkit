//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"io"
	"os"
	"slices"
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

	outboxclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	outboxstore "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox"
	outboxpg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/sqltx"
	elasticmqdb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/elasticmq"
	miniodb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/outboxtest"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/outbox"
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
	return time.Date(
		dateInput,
		1,
		dateInputAt,
		dateInputEnqueuedAt,
		enqueuedAtHour,
		enqueuedAtMinute,
		0,
		time.UTC,
	)
}

// relaySchema is a product-shaped outbox table whose route column carries the record
// attribute a routing sink reads.
const relaySchema = `
CREATE TABLE relay_outbox (
	id              uuid        PRIMARY KEY,
	lane            text        NOT NULL,
	route           text        NOT NULL DEFAULT '',
	aggregate_id    text        NOT NULL,
	tenant          text        NOT NULL DEFAULT '',
	payload         bytea       NOT NULL,
	status          text        NOT NULL DEFAULT 'pending',
	attempts        integer     NOT NULL DEFAULT 0,
	next_attempt_at timestamptz NOT NULL DEFAULT now(),
	last_error      text        NOT NULL DEFAULT '',
	created_at      timestamptz NOT NULL DEFAULT now(),
	sent_at         timestamptz
)`

// newOutboxStore starts Postgres, creates the relay's outbox table and returns the
// postgres-kind store over it, built by the outbox-store factory, with the
// producer-side insert and the transaction manager the relay commits through.
func newOutboxStore(
	t *testing.T,
) (
	store interfaces.OutboxStore,
	enqueue func(context.Context, *types.OutboxRecord) error,
	tx interfaces.TransactionManager,
) {
	t.Helper()

	ctx := context.Background()
	pg, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { pg.Close(context.Background()) })

	value, err := pg.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = value.Close() })

	_, err = value.ExecContext(ctx, relaySchema)
	require.NoError(t, err)

	store, err = outboxstore.NewFromConfig(ctx, &outboxstore.Config{
		Kind: outboxstore.KindPostgres,
		Postgres: outboxpg.Config{
			Table:            "relay_outbox",
			TenantColumn:     "tenant",
			AttributeColumns: map[string]string{types.OutboxRouteAttribute: "route"},
			Lease:            time.Minute,
		},
	}, outboxstore.Deps{DB: value})
	require.NoError(t, err)

	enqueue = func(ctx context.Context, rec *types.OutboxRecord) error {
		created := rec.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}

		_, err := value.ExecContext(ctx, `INSERT INTO relay_outbox
			(id, lane, route, aggregate_id, tenant, payload, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			rec.ID, rec.Lane, rec.Attributes[types.OutboxRouteAttribute], rec.Key,
			rec.Tenant, rec.Payload, created)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "relay test: enqueue")
		}

		return nil
	}

	return store, enqueue, sqltx.New(value)
}

// awsConfig is a static-credential AWS config for a local emulator.
func awsConfig(t *testing.T, key, secret string) aws.Config {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(textUsEast1),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(key, secret, ""),
		),
	)
	require.NoError(t, err)

	return cfg
}

// enqueueN enqueues n records on lane for tenant and returns their ids.
func enqueueN(
	t *testing.T,
	enqueue func(context.Context, *types.OutboxRecord) error,
	lane string,
	n int,
) []uuid.UUID {
	t.Helper()

	ids := make([]uuid.UUID, n)
	for i := range n {
		ids[i] = uuid.New()

		rec := types.OutboxRecord{
			ID: ids[i], Lane: lane, Tenant: textOrg1, Key: strconv.Itoa(i),
			Payload: []byte(`{"seq":` + strconv.Itoa(i) + `}`), CreatedAt: enqueuedAt(),
		}
		require.NoError(t, enqueue(context.Background(), &rec))
	}

	return ids
}

// drain runs the relay until the lane has nothing pending (bounded).
func drain(t *testing.T, relay *outbox.Relay, store interfaces.OutboxStore, lane string) {
	t.Helper()

	ctx := context.Background()
	for range iterationsDrain {
		require.NoError(t, relay.RunOnce(ctx))

		stats, err := store.Stats(ctx, lane)
		require.NoError(t, err)

		if stats.Pending == 0 {
			return
		}
	}

	t.Fatal("relay did not drain the lane")
}

// TestRelay_EndToEnd_PostgresToSQS drains a Postgres outbox into a real SQS
// (ElasticMQ) queue through the factory-built, decorated sink.
//
// Why this test is important:
//   - Only real infrastructure proves the whole path — claim, batch send, per-row
//     finalize — delivers every row exactly once and empties the lane.
//
// What it tests:
//   - 25 enqueued rows arrive as 25 messages whose bodies are the payloads, whose
//     outbox_id attributes are the row ids and whose partition_key attributes are
//     the rows' keys, unchanged; the lane ends with 0 pending.
func TestRelay_EndToEnd_PostgresToSQS(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	testRelayEndToEndPostgresToSQSStartsElasticMQ(t)
}

// testRelayEndToEndPostgresToSQSStartsElasticMQ continues
// testRelayEndToEndPostgresToSQS: starts elastic mq.
func testRelayEndToEndPostgresToSQSStartsElasticMQ(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	store, enqueue, transactionManager := newOutboxStore(t)
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
	cfg.SQS.Queue = "outbox-e2e"

	sink, err := outboxclient.NewFromConfig(ctx, &cfg, outboxclient.Deps{SQSAPI: api})
	require.NoError(t, err)

	ids := enqueueN(t, enqueue, textEvents, enqueueNInput)
	relay := outbox.NewRelay(
		new(outbox.DefaultRelayConfig(textEvents)),
		transactionManager,
		store,
		sink,
		nil,
		nil,
		nil,
	)

	testRelayEndToEndPostgresToSQSDrainsQueueExactlyOnce(
		t,
		store,
		api,
		created,
		ids,
		relay,
	)
}

// testRelayEndToEndPostgresToSQSDrainsQueueExactlyOnce continues
// testRelayEndToEndPostgresToSQSStartsElasticMQ: drains queue exactly once.
func testRelayEndToEndPostgresToSQSDrainsQueueExactlyOnce(
	t *testing.T,
	store interfaces.OutboxStore,
	api *awssqs.Client,
	created *awssqs.CreateQueueOutput,
	ids []uuid.UUID,
	relay *outbox.Relay,
) {
	t.Helper()

	ctx := context.Background()

	var err error

	drain(t, relay, store, textEvents)

	got, keys := map[string]string{}, map[string]string{}
	for len(got) < len(ids) {
		out, opErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl: created.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames: []string{"All"},
		})
		require.NoError(t, opErr)
		require.NotEmpty(t, out.Messages, "queue ran dry after %d messages", len(got))

		for _, message := range out.Messages {
			id := aws.ToString(message.MessageAttributes["outbox_id"].StringValue)
			require.NotContains(t, got, id, "duplicate delivery")

			got[id] = aws.ToString(message.Body)
			keys[id] = aws.ToString(
				message.MessageAttributes[interfaces.PartitionKeyAttribute].StringValue,
			)
		}
	}

	for i, id := range ids {
		require.Equal(t, `{"seq":`+strconv.Itoa(i)+`}`, got[id.String()])
		require.Equal(t, strconv.Itoa(i), keys[id.String()])
	}

	stats, err := store.Stats(ctx, textEvents)
	require.NoError(t, err)
	require.Equal(t, types.OutboxStats{}, stats)
}

// TestRelay_DrainsABacklogInOneRun drains a 1,000-row Postgres backlog with one
// RunOnce under a drain budget.
//
// Why this test is important:
//   - A burst larger than one batch must not wait one tick per batch; only the real
//     claim query (lease, SKIP LOCKED, ordering) proves the drain loop sees every row
//     and stops when a claim comes back short.
//
// What it tests:
//   - with BatchSize 100 and a one-minute DrainBudget, one RunOnce through the
//     shipped stub sink leaves the lane with 0 pending, while a run without a budget
//     leaves 900 of another 1,000-row lane pending.
func TestRelay_DrainsABacklogInOneRun(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	ctx := context.Background()
	store, enqueue, transactionManager := newOutboxStore(t)
	sink, err := outboxclient.NewFromConfig(
		ctx,
		new(outboxclient.DefaultConfig()),
		outboxclient.Deps{},
	)
	require.NoError(t, err)
	enqueueN(t, enqueue, textBurst, enqueueNInputRun)
	enqueueN(t, enqueue, textSingle, enqueueNInputRun)

	draining := outbox.DefaultRelayConfig(textBurst)

	draining.DrainBudget = time.Minute
	require.NoError(
		t,
		outbox.NewRelay(&draining, transactionManager, store, sink, nil, nil, nil).
			RunOnce(ctx),
	)

	single := outbox.DefaultRelayConfig(textSingle)
	require.NoError(
		t,
		outbox.NewRelay(&single, transactionManager, store, sink, nil, nil, nil).
			RunOnce(ctx),
	)

	burst, err := store.Stats(ctx, textBurst)
	require.NoError(t, err)
	require.Zero(t, burst.Pending)

	once, err := store.Stats(ctx, textSingle)
	require.NoError(t, err)
	require.Equal(t, wantPending, once.Pending)
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
	t.Parallel()

	requireIntegration(t)

	testRelayEndToEndPostgresToS3ObjectLockStartsMinIO(t)
}

// testRelayEndToEndPostgresToS3ObjectLockStartsMinIO continues
// testRelayEndToEndPostgresToS3ObjectLock: starts min io.
func testRelayEndToEndPostgresToS3ObjectLockStartsMinIO(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	store, enqueue, transactionManager := newOutboxStore(t)
	mio, err := miniodb.NewTestMinIO(ctx)
	require.NoError(t, err, "start minio")
	t.Cleanup(func() { mio.Close(context.Background()) })

	api := awss3.NewFromConfig(
		awsConfig(t, miniodb.RootUser, miniodb.RootPassword),
		func(o *awss3.Options) {
			o.BaseEndpoint, o.UsePathStyle = aws.String(mio.Endpoint()), true
		},
	)

	testRelayEndToEndPostgresToS3ObjectLockChecksObjectKeys(
		t,
		store,
		enqueue,
		transactionManager,
		api,
	)
}

// testRelayEndToEndPostgresToS3ObjectLockChecksObjectKeys continues
// testRelayEndToEndPostgresToS3ObjectLock: checks object keys.
func testRelayEndToEndPostgresToS3ObjectLockChecksObjectKeys(
	t *testing.T,
	store interfaces.OutboxStore,
	enqueue func(context.Context, *types.OutboxRecord) error,
	transactionManager interfaces.TransactionManager,
	api *awss3.Client,
) {
	t.Helper()

	ctx := context.Background()

	const bucket = "audit-worm"

	createLockedBucket(ctx, t, api, bucket)

	cfg := outboxclient.DefaultConfig()

	cfg.Kind = outboxclient.KindS3
	cfg.S3.Bucket = bucket

	sink, err := outboxclient.NewFromConfig(ctx, &cfg, outboxclient.Deps{S3API: api})
	require.NoError(t, err)

	ids := enqueueN(t, enqueue, textAudit, enqueueNInputKeys)
	relay := outbox.NewRelay(
		new(outbox.DefaultRelayConfig(textAudit)),
		transactionManager,
		store,
		sink,
		nil,
		nil,
		nil,
	)
	drain(t, relay, store, textAudit)

	keys := bucketKeys(ctx, t, api, bucket)

	day := enqueuedAt().Format("2006/01/02")
	want := make([]string, len(ids))

	for i, id := range ids {
		want[i] = "org-1/" + day + "/" + id.String()
	}

	slices.Sort(keys)
	slices.Sort(want)
	require.Equal(t, want, keys)

	for i, id := range ids {
		assertLockedObject(ctx, t, api, bucket, "org-1/"+day+"/"+id.String(), i)
	}

	stats, err := store.Stats(ctx, textAudit)
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
	t.Parallel()

	requireIntegration(t)

	testRelayEndToEndRoutesIsolateQueuesStartsElasticMQ(t)
}

// testRelayEndToEndRoutesIsolateQueuesStartsElasticMQ continues
// testRelayEndToEndRoutesIsolateQueues: starts elastic mq.
func testRelayEndToEndRoutesIsolateQueuesStartsElasticMQ(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	store, enqueue, transactionManager := newOutboxStore(t)
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
		createQueueInput := &awssqs.CreateQueueInput{QueueName: aws.String("q-" + route)}
		if route == "ordered" { // a FIFO queue rejects any entry without a group id
			createQueueInput.QueueName = aws.String("q-ordered.fifo")
			createQueueInput.Attributes = map[string]string{"FifoQueue": "true"}
		}

		routes[route] = aws.ToString(createQueueInput.QueueName)

		out, opErr := api.CreateQueue(ctx, createQueueInput)
		require.NoError(t, opErr)

		urls[route] = aws.ToString(out.QueueUrl)
	}

	cfg := outboxclient.DefaultConfig()

	cfg.Kind = outboxclient.KindSQS
	cfg.SQS.Routes = routes

	sink, err := outboxclient.NewFromConfig(ctx, &cfg, outboxclient.Deps{SQSAPI: api})
	require.NoError(t, err)

	testRelayEndToEndRoutesIsolateQueuesRunsRouting(
		t,
		store,
		enqueue,
		transactionManager,
		api,
		healthy,
		urls,
		sink,
	)
}

// testRelayEndToEndRoutesIsolateQueuesRunsRouting continues
// testRelayEndToEndRoutesIsolateQueues: runs routing.
func testRelayEndToEndRoutesIsolateQueuesRunsRouting(
	t *testing.T,
	store interfaces.OutboxStore,
	enqueue func(context.Context, *types.OutboxRecord) error,
	transactionManager interfaces.TransactionManager,
	api *awssqs.Client,
	healthy []string,
	urls map[string]string,
	sink interfaces.OutboxSink,
) {
	t.Helper()

	outboxtest.RunRouting(
		t,
		outboxtest.Harness{Store: store, Tx: transactionManager, Enqueue: enqueue},
		outboxtest.Routing{
			RunOnce: func(ctx context.Context, lane string) error {
				relay := outbox.NewRelay(
					new(
						outbox.DefaultRelayConfig(lane),
					),
					transactionManager,
					store,
					sink,
					nil,
					nil,
					nil,
				)

				return relay.RunOnce(ctx)
			},
			Healthy: healthy, Broken: "member_removed",
			Delivered: func(ctx context.Context, route string) ([]string, error) {
				return receiveAll(ctx, api, urls[route])
			},
		},
	)
}

// partitionedSchema is a product-shaped outbox table split into partitions by its
// part column.
const partitionedSchema = `
CREATE TABLE partitioned_outbox (
	id              uuid        PRIMARY KEY,
	lane            text        NOT NULL,
	aggregate_id    text        NOT NULL,
	part            integer     NOT NULL DEFAULT 0,
	payload         bytea       NOT NULL,
	status          text        NOT NULL DEFAULT 'pending',
	attempts        integer     NOT NULL DEFAULT 0,
	next_attempt_at timestamptz NOT NULL DEFAULT now(),
	last_error      text        NOT NULL DEFAULT '',
	created_at      timestamptz NOT NULL DEFAULT now(),
	sent_at         timestamptz
)`

// partitionCounts returns, per partition, how many rows are sent and how many of
// those were claimed more than once.
func partitionCounts(t *testing.T, db *sql.DB) (sent, reclaimed map[int]int) {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
SELECT part, count(*) FILTER (WHERE status = 'sent'),
       count(*) FILTER (WHERE status = 'sent' AND attempts > 1)
FROM partitioned_outbox GROUP BY part`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	sent, reclaimed = map[int]int{}, map[int]int{}

	for rows.Next() {
		var part, s, r int

		require.NoError(t, rows.Scan(&part, &s, &r))

		sent[part], reclaimed[part] = s, r
	}

	require.NoError(t, rows.Err())

	return sent, reclaimed
}

// TestRelay_PartitionsDrainIndependently tests relays over a partitioned lane.
//
// Why this test is important:
//   - One relay per partition is how a lane scales past one relay's throughput;
//     that holds only if the store writes each row to one partition, each relay
//     claims only its own, and a stalled partition never holds up another
//
// What it tests:
//   - 200 rows enqueued through the store over two partitions land in both
//     (types.OutboxPartition of their keys); running only partition 1's relay
//     sends exactly partition 1's rows and leaves partition 0 pending; then two
//     relays running concurrently leave every row sent, each claimed once
//   - a store over the same table narrowed to one partition reports partition 1's
//     pending rows as Stranded
func TestRelay_PartitionsDrainIndependently(t *testing.T) {
	t.Parallel()

	requireIntegration(t)

	ctx := context.Background()
	pg, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { pg.Close(context.Background()) })

	value, err := pg.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = value.Close() })

	_, err = value.ExecContext(ctx, partitionedSchema)
	require.NoError(t, err)

	store, err := outboxstore.NewFromConfig(ctx, &outboxstore.Config{
		Kind: outboxstore.KindPostgres,
		Postgres: outboxpg.Config{
			Table: "partitioned_outbox", PartitionColumn: "part", Partitions: 2,
			Lease: time.Minute,
		},
	}, outboxstore.Deps{DB: value})
	require.NoError(t, err)

	tx := sqltx.New(value)

	t.Helper()

	testRelayPartitionsDrainIndependentlyEnqueuesPartitions(t, value, store, tx)
}

// testRelayPartitionsDrainIndependentlyEnqueuesPartitions continues
// testRelayPartitionsDrainIndependently: enqueues partitions.
func testRelayPartitionsDrainIndependentlyEnqueuesPartitions(
	t *testing.T,
	value *sql.DB,
	store interfaces.OutboxStore,
	manager *sqltx.Manager,
) {
	t.Helper()

	ctx := context.Background()

	var err error

	want := map[int]int{}

	require.NoError(t, manager.WithTransaction(ctx, func(ctx context.Context) error {
		for i := range 200 {
			key := "doc-" + strconv.Itoa(i)
			want[types.OutboxPartition(key, 2)]++

			rec := types.OutboxRecord{Lane: "split", Key: key, Payload: []byte(key)}

			opErr := store.Enqueue(ctx, &rec)
			if opErr != nil {
				return opErr
			}
		}

		return nil
	}))
	require.Positive(t, want[0])
	require.Positive(t, want[1])

	narrowed, err := outboxstore.NewFromConfig(ctx, &outboxstore.Config{
		Kind: outboxstore.KindPostgres,
		Postgres: outboxpg.Config{
			Table: "partitioned_outbox", PartitionColumn: "part", Partitions: 1,
			Lease: time.Minute,
		},
	}, outboxstore.Deps{DB: value})
	require.NoError(t, err)

	testRelayPartitionsDrainIndependentlyDrainsEachPartition(
		t,
		value,
		store,
		manager,
		want,
		narrowed,
	)
}

// testRelayPartitionsDrainIndependentlyDrainsEachPartition continues
// testRelayPartitionsDrainIndependentlyEnqueuesPartitions: drains each partition.
func testRelayPartitionsDrainIndependentlyDrainsEachPartition(
	t *testing.T,
	value *sql.DB,
	store interfaces.OutboxStore,
	manager *sqltx.Manager,
	want map[int]int,
	narrowed interfaces.OutboxStore,
) {
	t.Helper()

	ctx := context.Background()

	var err error

	stats, err := narrowed.Stats(ctx, "split")
	require.NoError(t, err)
	require.Equal(
		t,
		want[1],
		stats.Stranded,
		"partition 1 is stranded at one partition",
	)

	sink, err := outboxclient.NewFromConfig(
		ctx,
		new(outboxclient.DefaultConfig()),
		outboxclient.Deps{},
	)
	require.NoError(t, err)

	relayFor := func(partition int) *outbox.Relay {
		cfg := outbox.DefaultRelayConfig("split")

		cfg.Partitions, cfg.Partition, cfg.DrainBudget = partitions, partition, time.Minute

		return outbox.NewRelay(&cfg, manager, store, sink, nil, nil, nil)
	}

	require.NoError(t, relayFor(1).RunOnce(ctx))

	sent, _ := partitionCounts(t, value)
	require.Equal(t, map[int]int{0: 0, 1: want[1]}, sent)

	done := make(chan error, makeInputPartition)

	for partition := range iterationsPartition {
		go func() { done <- relayFor(partition).RunOnce(ctx) }()
	}

	require.NoError(t, <-done)
	require.NoError(t, <-done)

	sent, reclaimed := partitionCounts(t, value)
	require.Equal(t, want, sent)
	require.Equal(t, map[int]int{0: 0, 1: 0}, reclaimed)
}

// createLockedBucket creates a bucket with default compliance-mode object lock.
func createLockedBucket(
	ctx context.Context, t *testing.T, api *awss3.Client, bucket string,
) {
	t.Helper()

	var err error

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
}

// bucketKeys lists the bucket's object keys.
func bucketKeys(
	ctx context.Context, t *testing.T, api *awss3.Client, bucket string,
) []string {
	t.Helper()

	listed, err := api.ListObjectsV2(
		ctx,
		&awss3.ListObjectsV2Input{Bucket: aws.String(bucket)},
	)
	require.NoError(t, err)

	keys := make([]string, 0, len(listed.Contents))
	for _, o := range listed.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}

	return keys
}

// assertLockedObject requires the object to be compliance-locked and to hold record seq.
func assertLockedObject(
	ctx context.Context, t *testing.T, api *awss3.Client, bucket, name string, seq int,
) {
	t.Helper()

	key := aws.String(name)
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
	require.Equal(t, `{"seq":`+strconv.Itoa(seq)+`}`, string(body))
}

// receiveAll drains the queue and returns the message bodies.
func receiveAll(ctx context.Context, api *awssqs.Client, url string) ([]string, error) {
	var got []string

	for {
		out, err := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(url),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     1,
		})
		if err != nil || len(out.Messages) == 0 {
			return got, apperr.Wrap(err, apperr.CodeUnavailable, "receive sqs messages")
		}

		for _, m := range out.Messages {
			got = append(got, aws.ToString(m.Body))
		}
	}
}

const (
	// dateInputAt is the date input at the tests use.
	dateInputAt = 2

	// dateInputEnqueuedAt is the date input enqueued at the tests use.
	dateInputEnqueuedAt = 3

	// enqueuedAtHour is the date input enqueued at13 the tests use.
	enqueuedAtHour = 4

	// enqueuedAtMinute is the date input enqueued at14 the tests use.
	enqueuedAtMinute = 5

	// enqueueNInput is the enqueue ninput the tests use.
	enqueueNInput = 25

	// enqueueNInputKeys is the enqueue ninput keys the tests use.
	enqueueNInputKeys = 5

	// enqueueNInputRun is the enqueue ninput run the tests use.
	enqueueNInputRun = 1000

	// iterationsDrain is the iterations drain the tests use.
	iterationsDrain = 10

	// iterationsPartition is the iterations partition the tests use.
	iterationsPartition = 2

	// makeInputPartition is the make input partition the tests use.
	makeInputPartition = 2

	// partitions is the partitions the tests use.
	partitions = 2

	// wantPending is the expected pending.
	wantPending = 900
)
