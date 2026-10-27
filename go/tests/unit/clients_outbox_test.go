package unit_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	smithy "github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// sinkRecords builds n records of tenant org-1, lane audit, created 2026-10-09 UTC.
func sinkRecords(n int) []types.OutboxRecord {
	recs := make([]types.OutboxRecord, n)
	for i := range recs {
		recs[i] = types.OutboxRecord{
			ID:        uuid.New(),
			Tenant:    "org-1",
			Lane:      "audit",
			Payload:   []byte(`{"n":` + strconv.Itoa(i) + `}`),
			Attempts:  1,
			CreatedAt: time.Date(2026, 10, 9, 23, 30, 0, 0, time.FixedZone("x", -3600)),
		}
	}
	return recs
}

// codesOf maps per-record results to their codes ("" for success).
func codesOf(results []error) []apperr.ErrorCode {
	out := make([]apperr.ErrorCode, len(results))
	for i, err := range results {
		if err != nil {
			out[i] = apperr.Code(err)
		}
	}
	return out
}

// TestOutboxSinkFromConfig_DefaultsToStubAndRejectsIncompleteBackends tests the sink
// factory.
//
// Why this test is important:
//   - The graph must boot with no queue or bucket, and a misconfigured sink must
//     fail at the composition root, not drop every outbox row at runtime
//
// What it tests:
//   - DefaultConfig (an empty kind) has the default key template; the factory
//     builds a *stub.Sink whose Send reports success for every record
//   - sqs without an API or queue, and s3 without an API, a bucket or "{id}" in
//     the key template are each INVALID_INPUT
//   - sqs and s3 with an API build *sqs.Sink and *s3.Sink
func TestOutboxSinkFromConfig_DefaultsToStubAndRejectsIncompleteBackends(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	cfg := outbox.DefaultConfig()
	assert.Equal(t, "{tenant}/{yyyy}/{mm}/{dd}/{id}", cfg.S3.KeyTemplate)

	sink, err := outbox.NewFromConfig(t.Context(), cfg, outbox.Deps{})
	require.NoError(t, err)
	assert.IsType(t, &stub.Sink{}, sink)
	assert.Equal(t, []error{nil, nil}, sink.Send(context.Background(), sinkRecords(2)))

	apis := outbox.Deps{
		SQSAPI: mocks.NewMockOutboxSQSAPI(ctrl),
		S3API:  mocks.NewMockOutboxS3API(ctrl),
	}
	broken := map[string]func(*outbox.Config, *outbox.Deps){
		"sqs no api": func(c *outbox.Config, d *outbox.Deps) {
			c.Kind, c.SQS.Queue, d.SQSAPI = outbox.KindSQS, "q", nil
		},
		"sqs no q": func(c *outbox.Config, _ *outbox.Deps) { c.Kind = outbox.KindSQS },
		"s3 no api": func(c *outbox.Config, d *outbox.Deps) {
			c.Kind, c.S3.Bucket, d.S3API = outbox.KindS3, "b", nil
		},
		"s3 no bkt": func(c *outbox.Config, _ *outbox.Deps) { c.Kind = outbox.KindS3 },
		"s3 no {id}": func(c *outbox.Config, _ *outbox.Deps) {
			c.Kind, c.S3.Bucket, c.S3.KeyTemplate = outbox.KindS3, "b", "{tenant}"
		},
	}
	for name, mutate := range broken {
		c, d := outbox.DefaultConfig(), apis
		mutate(&c, &d)
		_, err := outbox.NewFromConfig(t.Context(), c, d)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}

	sqsCfg := outbox.DefaultConfig()
	sqsCfg.Kind, sqsCfg.SQS = outbox.KindSQS, outboxsqs.Config{Queue: "q"}
	sqsSink, err := outbox.NewFromConfig(t.Context(), sqsCfg, apis)
	require.NoError(t, err)
	assert.IsType(t, &outboxsqs.Sink{}, sqsSink)

	s3Cfg := outbox.DefaultConfig()
	s3Cfg.Kind, s3Cfg.S3.Bucket = outbox.KindS3, "b"
	s3Sink, err := outbox.NewFromConfig(t.Context(), s3Cfg, apis)
	require.NoError(t, err)
	assert.IsType(t, &outboxs3.Sink{}, s3Sink)
}

// TestSQSSink_MapsPartialBatchFailureToRecords tests SQS per-entry result mapping.
//
// Why this test is important:
//   - SendMessageBatch succeeds per entry; the relay must learn exactly which
//     records failed (and whether retrying can help) to avoid both duplicates
//     and losses
//
// What it tests:
//   - 12 records go out as batches of 10 and 2 against the queue URL resolved
//     once (GetQueueUrl "audit-events", then cached)
//   - each entry carries Id = its batch index, the payload as the body and the
//     outbox_id, tenant and lane string attributes
//   - in batch 1, entry 3 (sender fault) is INVALID_INPUT, entry 7 (server
//     fault) UNAVAILABLE, and the rest succeed; batch 2's request error fails
//     both its records UNAVAILABLE
func TestSQSSink_MapsPartialBatchFailureToRecords(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(12)
	url := "http://sqs.local/000000000000/audit-events"
	var first *awssqs.SendMessageBatchInput

	api.EXPECT().
		GetQueueUrl(
			gomock.Any(), &awssqs.GetQueueUrlInput{QueueName: aws.String("audit-events")},
		).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String(url)}, nil).
		Times(1)
	successful := make([]sqstypes.SendMessageBatchResultEntry, 0, 8)
	for _, id := range []string{"0", "1", "2", "4", "5", "6", "8", "9"} {
		successful = append(
			successful,
			sqstypes.SendMessageBatchResultEntry{Id: aws.String(id)},
		)
	}
	gomock.InOrder(
		api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				in *awssqs.SendMessageBatchInput,
				_ ...func(*awssqs.Options),
			) (*awssqs.SendMessageBatchOutput, error) {
				first = in
				return &awssqs.SendMessageBatchOutput{
					Successful: successful,
					Failed: []sqstypes.BatchResultErrorEntry{
						{
							Id:          aws.String("3"),
							SenderFault: true,
							Code:        aws.String("InvalidMessageContents"),
						},
						{
							Id:          aws.String("7"),
							SenderFault: false,
							Code:        aws.String("InternalError"),
						},
					},
				}, nil
			}),
		api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).
			Return(
				nil, &smithy.GenericAPIError{Code: "ServiceUnavailable", Fault: smithy.FaultServer},
			),
	)

	cfg := outbox.DefaultConfig()
	cfg.Kind, cfg.SQS = outbox.KindSQS, outboxsqs.Config{Queue: "audit-events"}
	sink, err := outbox.NewFromConfig(t.Context(), cfg, outbox.Deps{SQSAPI: api})
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	u := apperr.CodeUnavailable
	assert.Equal(
		t,
		[]apperr.ErrorCode{
			"",
			"",
			"",
			apperr.CodeInvalidInput,
			"",
			"",
			"",
			u,
			"",
			"",
			u,
			u,
		},
		codesOf(results),
	)
	require.NotNil(t, first)
	assert.Equal(t, url, aws.ToString(first.QueueUrl))
	require.Len(t, first.Entries, 10)
	assert.Equal(t, "3", aws.ToString(first.Entries[3].Id))
	assert.Equal(t, `{"n":3}`, aws.ToString(first.Entries[3].MessageBody))
	assert.Equal(t, map[string]sqstypes.MessageAttributeValue{
		"outbox_id": {
			DataType:    aws.String("String"),
			StringValue: aws.String(recs[3].ID.String()),
		},
		"tenant": {DataType: aws.String("String"), StringValue: aws.String("org-1")},
		"lane":   {DataType: aws.String("String"), StringValue: aws.String("audit")},
	}, first.Entries[3].MessageAttributes)
}

// TestSQSSink_RoutesByKeyAndIsolatesFailingQueue tests multi-queue routing.
//
// Why this test is important:
//   - One relay drains several event types to their own queues; a queue that is
//     missing or failing must fail only its own rows, never stall or fail the
//     rows bound for healthy queues
//
// What it tests:
//   - a record's Attributes["route"] picks its queue from Config.Routes; a record
//     with no route goes to Config.Queue; a route not in the map is INVALID_INPUT
//   - each queue is resolved and sent separately: a queue whose URL lookup fails
//     and a queue whose batch send fails make only their own rows UNAVAILABLE
//   - a route mapped to an empty queue name is rejected at construction
func TestSQSSink_RoutesByKeyAndIsolatesFailingQueue(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(6)
	routes := []string{"standard", "deletion", "facet", "standard", "", "bogus"}
	for i, r := range routes {
		if r != "" {
			recs[i].Attributes = map[string]string{types.OutboxRouteAttribute: r}
		}
	}
	urlOf := map[string]string{
		"q-standard": "u-standard",
		"q-facet":    "u-facet",
		"q-default":  "u-default",
	}
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.GetQueueUrlInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.GetQueueUrlOutput, error) {
			if u, ok := urlOf[aws.ToString(in.QueueName)]; ok {
				return &awssqs.GetQueueUrlOutput{QueueUrl: aws.String(u)}, nil
			}
			return nil, &smithy.GenericAPIError{
				Code:  "AWS.SimpleQueueService.NonExistentQueue",
				Fault: smithy.FaultClient,
			}
		})
	sentTo := map[string][]string{}
	var mu sync.Mutex
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			url := aws.ToString(in.QueueUrl)
			if url == "u-facet" {
				return nil, &smithy.GenericAPIError{
					Code:  "ServiceUnavailable",
					Fault: smithy.FaultServer,
				}
			}
			out := &awssqs.SendMessageBatchOutput{}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range in.Entries {
				sentTo[url] = append(sentTo[url], aws.ToString(e.MessageBody))
				out.Successful = append(
					out.Successful,
					sqstypes.SendMessageBatchResultEntry{Id: e.Id},
				)
			}
			return out, nil
		})

	cfg := outbox.DefaultConfig()
	cfg.Kind = outbox.KindSQS
	cfg.SQS = outboxsqs.Config{Queue: "q-default", Routes: map[string]string{
		"standard": "q-standard", "deletion": "q-deletion", "facet": "q-facet",
	}}
	sink, err := outbox.NewFromConfig(t.Context(), cfg, outbox.Deps{SQSAPI: api})
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	u := apperr.CodeUnavailable
	assert.Equal(
		t,
		[]apperr.ErrorCode{
			"",
			apperr.CodeInvalidInput,
			u,
			"",
			"",
			apperr.CodeInvalidInput,
		},
		codesOf(results),
	)
	assert.Equal(t, map[string][]string{
		"u-standard": {`{"n":0}`, `{"n":3}`},
		"u-default":  {`{"n":4}`},
	}, sentTo)

	bad := cfg
	bad.SQS.Routes = map[string]string{"standard": ""}
	_, err = outbox.NewFromConfig(t.Context(), bad, outbox.Deps{SQSAPI: api})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestS3Sink_KeysByTemplateAndAppliesKMS tests the S3 sink's object writes.
//
// Why this test is important:
//   - An archive keyed wrongly cannot be found or partitioned, a missing
//     integrity checksum is rejected by Object Lock buckets, and a record that asks for
//     SSE-KMS must never be written with the bucket default
//
// What it tests:
//   - template "audit/{tenant}/{yyyy}/{mm}/{dd}/{id}.json" keys a record created
//     2026-10-09 23:30 at UTC-1 as audit/org-1/2026/10/10/<id>.json (UTC date)
//   - each PutObject targets bucket "archive" with the payload as body and
//     ChecksumAlgorithm SHA256 with ChecksumSHA256 = base64(sha256(payload)),
//     and no Content-MD5 (Object Lock accepts either; MD5 is not used)
//   - a record with Attributes["kms_key_id"] gets SSE aws:kms with that key; one
//     without gets no SSE fields
//   - an AccessDenied client fault is FORBIDDEN for its record only
func TestS3Sink_KeysByTemplateAndAppliesKMS(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxS3API(ctrl)
	recs := sinkRecords(3)
	recs[1].Attributes = map[string]string{
		"kms_key_id": "arn:aws:kms:us-east-1:000000000000:key/k-1",
	}
	type put struct {
		in   *awss3.PutObjectInput
		body string
	}
	var puts []put

	api.EXPECT().PutObject(gomock.Any(), gomock.Any()).Times(3).
		DoAndReturn(func(
			_ context.Context,
			in *awss3.PutObjectInput,
			_ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			body, err := io.ReadAll(in.Body)
			require.NoError(t, err)
			puts = append(puts, put{in: in, body: string(body)})
			if len(puts) == 3 {
				return nil, &smithy.GenericAPIError{
					Code:  "AccessDenied",
					Fault: smithy.FaultClient,
				}
			}
			return &awss3.PutObjectOutput{}, nil
		})

	cfg := outbox.DefaultConfig()
	cfg.Kind = outbox.KindS3
	cfg.S3 = outboxs3.Config{
		Bucket:      "archive",
		KeyTemplate: "audit/{tenant}/{yyyy}/{mm}/{dd}/{id}.json",
	}
	sink, err := outbox.NewFromConfig(t.Context(), cfg, outbox.Deps{S3API: api})
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	assert.Equal(t, []apperr.ErrorCode{"", "", apperr.CodeForbidden}, codesOf(results))
	require.Len(t, puts, 3)
	for i, p := range puts {
		sum := sha256.Sum256(recs[i].Payload)
		assert.Equal(t, "archive", aws.ToString(p.in.Bucket))
		assert.Equal(
			t,
			"audit/org-1/2026/10/10/"+recs[i].ID.String()+".json",
			aws.ToString(p.in.Key),
		)
		assert.Equal(t, string(recs[i].Payload), p.body)
		assert.Equal(
			t,
			base64.StdEncoding.EncodeToString(sum[:]),
			aws.ToString(p.in.ChecksumSHA256),
		)
		assert.Equal(t, s3types.ChecksumAlgorithmSha256, p.in.ChecksumAlgorithm)
		assert.Nil(t, p.in.ContentMD5)
	}
	assert.Equal(t, s3types.ServerSideEncryption(""), puts[0].in.ServerSideEncryption)
	assert.Nil(t, puts[0].in.SSEKMSKeyId)
	assert.Equal(t, s3types.ServerSideEncryptionAwsKms, puts[1].in.ServerSideEncryption)
	assert.Equal(
		t,
		"arn:aws:kms:us-east-1:000000000000:key/k-1",
		aws.ToString(puts[1].in.SSEKMSKeyId),
	)
}

// TestOutboxSinkDecorators_RetryOnlyTransientWithFullBody tests the sink's client stack.
//
// Why this test is important:
//   - With retries enabled, a server fault must be retried with the whole
//     payload (a consumed body would write an empty object), while a client
//     fault must not be retried at all
//
// What it tests:
//   - a PutObject failing with a server fault then succeeding is called twice,
//     the second attempt reads the full payload, and the record succeeds
//   - a PutObject failing with an InvalidArgument client fault is called once
//     and the record is INVALID_INPUT
func TestOutboxSinkDecorators_RetryOnlyTransientWithFullBody(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxS3API(ctrl)
	recs := sinkRecords(2)
	var bodies []string
	read := func(in *awss3.PutObjectInput) {
		b, err := io.ReadAll(in.Body)
		require.NoError(t, err)
		bodies = append(bodies, string(b))
	}

	gomock.InOrder(
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				in *awss3.PutObjectInput,
				_ ...func(*awss3.Options),
			) (*awss3.PutObjectOutput, error) {
				read(in)
				return nil, &smithy.GenericAPIError{
					Code:  "InternalError",
					Fault: smithy.FaultServer,
				}
			}),
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				in *awss3.PutObjectInput,
				_ ...func(*awss3.Options),
			) (*awss3.PutObjectOutput, error) {
				read(in)
				return &awss3.PutObjectOutput{}, nil
			}),
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
			Return(
				nil, &smithy.GenericAPIError{Code: "InvalidArgument", Fault: smithy.FaultClient},
			).
			Times(1),
	)

	cfg := outbox.DefaultConfig()
	cfg.Kind, cfg.S3.Bucket = outbox.KindS3, "archive"
	on := true
	decs := decoration.DefaultConfig()
	decs.Clients.Overrides["outbox.s3"] = decoration.Merge(
		decs.Clients.Overrides["outbox.s3"],
		decoration.Layers{Retry: decoration.Retry{
			Enabled: &on,
			RetryConfig: resilience.RetryConfig{
				MaxRetries:      3,
				InitialInterval: time.Millisecond,
				MaxInterval:     time.Millisecond,
			},
		}},
	)
	sink, err := outbox.NewFromConfig(t.Context(), cfg, outbox.Deps{
		Decorations: decorate.NewDecorations(&decs, decorate.Collaborators{}),
		S3API:       api,
	})
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	assert.Equal(t, []apperr.ErrorCode{"", apperr.CodeInvalidInput}, codesOf(results))
	assert.Equal(t, []string{string(recs[0].Payload), string(recs[0].Payload)}, bodies)
}

// TestSQSSink_FIFOQueueCarriesGroupAndDedupIDs tests delivery to a FIFO queue.
//
// Why this test is important:
//   - SQS rejects every FIFO entry without a MessageGroupId, so without one each
//     record bound for a FIFO queue would back off to parking; the record's Key
//     is the ordering group and its ID the deduplication id
//
// What it tests:
//   - an entry for "events.fifo" carries MessageGroupId = the record's Key and
//     MessageDeduplicationId = its ID; a record with no Key is grouped by tenant
//   - an entry for a standard queue carries neither
func TestSQSSink_FIFOQueueCarriesGroupAndDedupIDs(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(3)
	recs[0].Key = "doc-7"
	recs[0].Attributes = map[string]string{types.OutboxRouteAttribute: "ordered"}
	recs[1].Attributes = map[string]string{types.OutboxRouteAttribute: "ordered"}
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).Times(2).
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.GetQueueUrlInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.GetQueueUrlOutput, error) {
			return &awssqs.GetQueueUrlOutput{
				QueueUrl: aws.String("u-" + aws.ToString(in.QueueName)),
			}, nil
		})
	sent := map[string][]sqstypes.SendMessageBatchRequestEntry{}
	var mu sync.Mutex
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).Times(2).
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			out := &awssqs.SendMessageBatchOutput{}
			mu.Lock()
			defer mu.Unlock()
			sent[aws.ToString(in.QueueUrl)] = in.Entries
			for _, e := range in.Entries {
				out.Successful = append(
					out.Successful,
					sqstypes.SendMessageBatchResultEntry{Id: e.Id},
				)
			}
			return out, nil
		})

	sink, err := outboxsqs.New(outboxsqs.Config{
		Queue: "plain", Routes: map[string]string{"ordered": "events.fifo"},
	}, api)
	require.NoError(t, err)

	assert.Equal(t, []error{nil, nil, nil}, sink.Send(context.Background(), recs))
	fifo := sent["u-events.fifo"]
	require.Len(t, fifo, 2)
	assert.Equal(t, "doc-7", aws.ToString(fifo[0].MessageGroupId))
	assert.Equal(t, recs[0].ID.String(), aws.ToString(fifo[0].MessageDeduplicationId))
	assert.Equal(t, "org-1", aws.ToString(fifo[1].MessageGroupId))
	assert.Equal(t, recs[1].ID.String(), aws.ToString(fifo[1].MessageDeduplicationId))
	plain := sent["u-plain"]
	require.Len(t, plain, 1)
	assert.Nil(t, plain[0].MessageGroupId)
	assert.Nil(t, plain[0].MessageDeduplicationId)
}

// TestSQSSink_PacksBatchesWithinSQSByteLimits tests the sink against SQS's size limits.
//
// Why this test is important:
//   - SendMessageBatch rejects a whole request over 1 MiB, so packing ten large
//     records into one request fails all ten on every attempt and parks them
//   - a single message over 256 KiB can never be sent, so retrying it only delays
//     parking it
//
// What it tests:
//   - ten ~200 KiB records go out in several SendMessageBatch requests, each within
//     1 MiB of message bodies and attributes, and every record is sent once
//   - a record over 256 KiB fails CodeInvalidInput without reaching SQS, while the
//     other records in the same Send still go out
func TestSQSSink_PacksBatchesWithinSQSByteLimits(t *testing.T) {
	t.Parallel()
	const (
		maxRequestBytes = 1 << 20
		maxMessageBytes = 256 << 10
	)
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(11)
	for i := range recs[:10] {
		recs[i].Payload = []byte(strings.Repeat("a", 200<<10))
	}
	recs[10].Payload = []byte(strings.Repeat("b", maxMessageBytes+1))
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("u")}, nil)
	var requests [][]sqstypes.SendMessageBatchRequestEntry
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			requests = append(requests, in.Entries)
			out := &awssqs.SendMessageBatchOutput{}
			for _, e := range in.Entries {
				out.Successful = append(
					out.Successful,
					sqstypes.SendMessageBatchResultEntry{Id: e.Id},
				)
			}
			return out, nil
		})

	sink, err := outboxsqs.New(outboxsqs.Config{Queue: "q"}, api)
	require.NoError(t, err)
	results := sink.Send(context.Background(), recs)

	want := make([]apperr.ErrorCode, len(recs))
	want[10] = apperr.CodeInvalidInput
	assert.Equal(t, want, codesOf(results))
	require.Greater(t, len(requests), 1)
	sent := 0
	for _, entries := range requests {
		size := 0
		for _, e := range entries {
			size += len(aws.ToString(e.MessageBody))
			for name, v := range e.MessageAttributes {
				size += len(name) + len(aws.ToString(v.DataType)) +
					len(aws.ToString(v.StringValue))
			}
		}
		assert.LessOrEqual(t, size, maxRequestBytes)
		sent += len(entries)
	}
	assert.Equal(t, 10, sent, "every sendable record sent exactly once")
}

// TestSQSSink_OmitsEmptyStringAttributes tests that no empty attribute is sent.
//
// Why this test is important:
//   - SQS rejects a message attribute with an empty value, so a record from a
//     store without tenants would fail on every attempt and park
//
// What it tests:
//   - a record with no Tenant carries only the outbox_id and lane attributes
func TestSQSSink_OmitsEmptyStringAttributes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(1)
	recs[0].Tenant = ""
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("u")}, nil)
	var sent *awssqs.SendMessageBatchInput
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			sent = in
			return &awssqs.SendMessageBatchOutput{
				Successful: []sqstypes.SendMessageBatchResultEntry{{Id: aws.String("0")}},
			}, nil
		})

	sink, err := outboxsqs.New(outboxsqs.Config{Queue: "q"}, api)
	require.NoError(t, err)

	assert.Equal(t, []error{nil}, sink.Send(context.Background(), recs))
	require.NotNil(t, sent)
	assert.Equal(t, map[string]sqstypes.MessageAttributeValue{
		"outbox_id": {
			DataType:    aws.String("String"),
			StringValue: aws.String(recs[0].ID.String()),
		},
		"lane": {DataType: aws.String("String"), StringValue: aws.String("audit")},
	}, sent.Entries[0].MessageAttributes)
}

// TestS3Sink_KeyTemplateSubstitutesRecordKey tests the {key} key-template placeholder.
//
// Why this test is important:
//   - A consumer that partitions its archive by the record's Key needs the key in
//     the object path; an unsubstituted placeholder would write every record of
//     a tenant and day under one literal "{key}" prefix
//
// What it tests:
//   - template "{tenant}/{key}/{id}" keys a record with Key "doc-7" as
//     org-1/doc-7/<id>
func TestS3Sink_KeyTemplateSubstitutesRecordKey(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxS3API(ctrl)
	recs := sinkRecords(1)
	recs[0].Key = "doc-7"
	var key string
	api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			in *awss3.PutObjectInput,
			_ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			key = aws.ToString(in.Key)
			return &awss3.PutObjectOutput{}, nil
		})

	sink, err := outboxs3.New(
		outboxs3.Config{Bucket: "archive", KeyTemplate: "{tenant}/{key}/{id}"},
		api,
	)
	require.NoError(t, err)

	assert.Equal(t, []error{nil}, sink.Send(context.Background(), recs))
	assert.Equal(t, "org-1/doc-7/"+recs[0].ID.String(), key)
}
