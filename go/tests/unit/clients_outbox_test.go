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
	"github.com/aws/smithy-go/middleware"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// sinkRecords builds n records of tenant org-1, lane audit, created 2026-10-09 UTC.
func sinkRecords(n int) []types.OutboxRecord {
	recs := make([]types.OutboxRecord, n)
	for i := range recs {
		recs[i] = types.OutboxRecord{
			ID:       uuid.New(),
			Tenant:   textOrg1,
			Lane:     textAudit,
			Payload:  []byte(`{"n":` + strconv.Itoa(i) + `}`),
			Attempts: 1,
			CreatedAt: time.Date(
				dateInput,
				dateInputRecords,
				dateInputSinkRecords,
				sinkRecordsHour,
				sinkRecordsMinute,
				0,
				0,
				time.FixedZone(textX, -3600),
			),
			Attributes: nil,
			Key:        "",
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

	sink, err := outbox.NewFromConfig(t.Context(), &cfg, stdx.Zero[outbox.Deps]())
	require.NoError(t, err)
	assert.IsType(t, &stub.Sink{}, sink)
	assert.Equal(
		t,
		[]error{nil, nil},
		sink.Send(context.Background(), sinkRecords(sinkRecordsInput)),
	)

	apis := outbox.Deps{
		SQSAPI:      mocks.NewMockOutboxSQSAPI(ctrl),
		S3API:       mocks.NewMockOutboxS3API(ctrl),
		Decorations: decorate.Decorations{},
	}
	broken := map[string]func(*outbox.Config, *outbox.Deps){
		"sqs no api": func(c *outbox.Config, d *outbox.Deps) {
			c.Kind, c.SQS.Queue, d.SQSAPI = outbox.KindSQS, textQ, nil
		},
		"sqs no q": func(c *outbox.Config, _ *outbox.Deps) { c.Kind = outbox.KindSQS },
		"s3 no api": func(c *outbox.Config, d *outbox.Deps) {
			c.Kind, c.S3.Bucket, d.S3API = outbox.KindS3, textB, nil
		},
		"s3 no bkt": func(c *outbox.Config, _ *outbox.Deps) { c.Kind = outbox.KindS3 },
		"s3 no {id}": func(c *outbox.Config, _ *outbox.Deps) {
			c.Kind, c.S3.Bucket, c.S3.KeyTemplate = outbox.KindS3, textB, "{tenant}"
		},
	}

	testOutboxSinkFromConfigDefaultsToStubAndRejectsIncompleteBackendsSQSBackend(
		t,
		apis,
		broken,
	)
}

// testOutboxSinkFromConfigDefaultsToStubAndRejectsIncompleteBackendsSQSBackend
// continues TestOutboxSinkFromConfig_DefaultsToStubAndRejectsIncompleteBackends.
func testOutboxSinkFromConfigDefaultsToStubAndRejectsIncompleteBackendsSQSBackend(
	t *testing.T,
	apis outbox.Deps,
	broken map[string]func(*outbox.Config, *outbox.Deps),
) {
	t.Helper()

	for name, mutate := range broken {
		c, d := outbox.DefaultConfig(), apis
		mutate(&c, &d)

		_, opErr := outbox.NewFromConfig(t.Context(), &c, d)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(opErr), name)
	}

	sqsCfg := outbox.DefaultConfig()

	sqsCfg.Kind, sqsCfg.SQS = outbox.KindSQS, outboxsqs.Config{
		Queue:      textQ,
		Routes:     nil,
		FairQueues: nil,
	}

	sqsSink, err := outbox.NewFromConfig(t.Context(), &sqsCfg, apis)
	require.NoError(t, err)
	assert.IsType(t, &outboxsqs.Sink{}, sqsSink)

	s3Cfg := outbox.DefaultConfig()

	s3Cfg.Kind, s3Cfg.S3.Bucket = outbox.KindS3, textB

	s3Sink, err := outbox.NewFromConfig(t.Context(), &s3Cfg, apis)
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
	recs := sinkRecords(sinkRecordsInputRecords)
	url := "http://sqs.local/000000000000/audit-events"

	var first *awssqs.SendMessageBatchInput

	api.EXPECT().
		GetQueueUrl(
			gomock.Any(),
			&awssqs.GetQueueUrlInput{
				QueueName:              aws.String("audit-events"),
				QueueOwnerAWSAccountId: nil,
			},
		).
		Return(&awssqs.GetQueueUrlOutput{
			QueueUrl:       aws.String(url),
			ResultMetadata: middleware.Metadata{},
		}, nil).
		Times(1)

	successful := make([]sqstypes.SendMessageBatchResultEntry, 0, makeInputRecords)
	for _, id := range []string{text0, "1", "2", "4", "5", "6", "8", "9"} {
		successful = append(
			successful,
			fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
				row.Id = aws.String(id)
			}),
		)
	}

	testSQSSinkMapsPartialBatchFailureToRecordsExpectsBatches(
		t,
		api,
		recs,
		url,
		first,
		successful,
	)
}

// testSQSSinkMapsPartialBatchFailureToRecordsExpectsBatches continues
// testSQSSinkMapsPartialBatchFailureToRecords: expects batches.
func testSQSSinkMapsPartialBatchFailureToRecordsExpectsBatches(
	t *testing.T,
	api *mocks.MockOutboxSQSAPI,
	recs []types.OutboxRecord,
	url string,
	first *awssqs.SendMessageBatchInput,
	successful []sqstypes.SendMessageBatchResultEntry,
) {
	t.Helper()

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
							Message:     nil,
						},
						{
							Id:          aws.String("7"),
							SenderFault: false,
							Code:        aws.String("InternalError"),
							Message:     nil,
						},
					},
					ResultMetadata: middleware.Metadata{},
				}, nil
			}),
		api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).
			Return(
				nil, &smithy.GenericAPIError{
					Code:  "ServiceUnavailable",
					Fault: smithy.FaultServer, Message: "",
				},
			),
	)

	cfg := outbox.DefaultConfig()

	testSQSSinkMapsPartialBatchFailureToRecordsChecksRecords(
		t,
		api,
		recs,
		url,
		&first,
		&cfg,
	)
}

// testSQSSinkMapsPartialBatchFailureToRecordsChecksRecords continues
// testSQSSinkMapsPartialBatchFailureToRecordsExpectsBatches: checks records.
func testSQSSinkMapsPartialBatchFailureToRecordsChecksRecords(
	t *testing.T,
	api *mocks.MockOutboxSQSAPI,
	recs []types.OutboxRecord,
	url string,
	first **awssqs.SendMessageBatchInput,
	cfgRef *outbox.Config,
) {
	t.Helper()

	cfg := *cfgRef

	cfg.Kind, cfg.SQS = outbox.KindSQS, outboxsqs.Config{
		Queue:      "audit-events",
		Routes:     nil,
		FairQueues: nil,
	}

	sink, err := outbox.NewFromConfig(
		t.Context(),
		&cfg,
		outbox.Deps{SQSAPI: api, Decorations: decorate.Decorations{}, S3API: nil},
	)
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	errorCode := apperr.CodeUnavailable

	testSQSSinkMapsPartialBatchFailureToRecordsChecksRecordsFirstRequest(
		t,
		recs,
		url,
		first,
		results,
		errorCode,
	)
}

// testSQSSinkMapsPartialBatchFailureToRecordsChecksRecordsFirstRequest continues
// testSQSSinkMapsPartialBatchFailureToRecordsChecksRecords.
func testSQSSinkMapsPartialBatchFailureToRecordsChecksRecordsFirstRequest(
	t *testing.T,
	recs []types.OutboxRecord,
	url string,
	first **awssqs.SendMessageBatchInput,
	results []error,
	errorCode apperr.ErrorCode,
) {
	t.Helper()

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
			errorCode,
			"",
			"",
			errorCode,
			errorCode,
		},
		codesOf(results),
	)
	require.NotNil(t, (*first))
	assert.Equal(t, url, aws.ToString((*first).QueueUrl))
	require.Len(t, (*first).Entries, wantEntriesLen)
	assert.Equal(t, "3", aws.ToString((*first).Entries[3].Id))
	assert.Equal(t, `{"n":3}`, aws.ToString((*first).Entries[3].MessageBody))
	assert.Equal(t, map[string]sqstypes.MessageAttributeValue{
		"outbox_id": fixtures.Build(func(row *sqstypes.MessageAttributeValue) {
			row.DataType = aws.String("String")
			row.StringValue = aws.String(recs[3].ID.String())
		}),
		textTenant: fixtures.Build(func(row *sqstypes.MessageAttributeValue) {
			row.DataType = aws.String("String")
			row.StringValue = aws.String(textOrg1)
		}),
		textLane: fixtures.Build(func(row *sqstypes.MessageAttributeValue) {
			row.DataType = aws.String("String")
			row.StringValue = aws.String(textAudit)
		}),
	}, (*first).Entries[3].MessageAttributes)
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
	recs := sinkRecords(sinkRecordsInputQueue)
	routes := []string{textStandard, "deletion", "facet", textStandard, "", textBogus}

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
				return &awssqs.GetQueueUrlOutput{
					QueueUrl:       aws.String(u),
					ResultMetadata: middleware.Metadata{},
				}, nil
			}

			return nil, &smithy.GenericAPIError{
				Code:    "AWS.SimpleQueueService.NonExistentQueue",
				Fault:   smithy.FaultClient,
				Message: "",
			}
		})

	sentTo := map[string][]string{}

	testSQSSinkRoutesByKeyAndIsolatesFailingQueueExpectsBatches(t, api, recs, sentTo)
}

// testSQSSinkRoutesByKeyAndIsolatesFailingQueueExpectsBatches continues
// testSQSSinkRoutesByKeyAndIsolatesFailingQueue: expects batches.
func testSQSSinkRoutesByKeyAndIsolatesFailingQueueExpectsBatches(
	t *testing.T,
	api *mocks.MockOutboxSQSAPI,
	recs []types.OutboxRecord,
	sentTo map[string][]string,
) {
	t.Helper()

	var lock sync.Mutex

	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(
			_ context.Context,
			sendMessageBatchInput *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			url := aws.ToString(sendMessageBatchInput.QueueUrl)
			if url == "u-facet" {
				return nil, &smithy.GenericAPIError{
					Code:    "ServiceUnavailable",
					Fault:   smithy.FaultServer,
					Message: "",
				}
			}

			out := new(awssqs.SendMessageBatchOutput)

			lock.Lock()
			defer lock.Unlock()

			for _, e := range sendMessageBatchInput.Entries {
				sentTo[url] = append(sentTo[url], aws.ToString(e.MessageBody))
				out.Successful = append(
					out.Successful,
					fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
						row.Id = e.Id
					}),
				)
			}

			return out, nil
		})

	testSQSSinkRoutesByKeyAndIsolatesFailingQueueChecksRouting(t, api, recs, sentTo)
}

// testSQSSinkRoutesByKeyAndIsolatesFailingQueueChecksRouting continues
// testSQSSinkRoutesByKeyAndIsolatesFailingQueueExpectsBatches: checks routing.
func testSQSSinkRoutesByKeyAndIsolatesFailingQueueChecksRouting(
	t *testing.T,
	api *mocks.MockOutboxSQSAPI,
	recs []types.OutboxRecord,
	sentTo map[string][]string,
) {
	t.Helper()

	cfg := outbox.DefaultConfig()

	cfg.Kind = outbox.KindSQS
	cfg.SQS = outboxsqs.Config{
		Queue: "q-default", Routes: map[string]string{
			textStandard: "q-standard", "deletion": "q-deletion", "facet": "q-facet",
		},
		FairQueues: nil,
	}

	sink, err := outbox.NewFromConfig(
		t.Context(),
		&cfg,
		outbox.Deps{SQSAPI: api, Decorations: decorate.Decorations{}, S3API: nil},
	)
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	errorCode := apperr.CodeUnavailable
	assert.Equal(
		t,
		[]apperr.ErrorCode{
			"",
			apperr.CodeInvalidInput,
			errorCode,
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

	bad.SQS.Routes = map[string]string{textStandard: ""}
	_, err = outbox.NewFromConfig(
		t.Context(),
		&bad,
		outbox.Deps{SQSAPI: api, Decorations: decorate.Decorations{}, S3API: nil},
	)
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
	recs := sinkRecords(sinkRecordsInputKMS)

	recs[1].Attributes = map[string]string{
		"kms_key_id": "arn:aws:kms:us-east-1:000000000000:key/k-1",
	}

	testS3SinkKeysByTemplateAndAppliesKMSChecksPuts(t, api, recs)
}

// testS3SinkKeysByTemplateAndAppliesKMSChecksPuts continues
// testS3SinkKeysByTemplateAndAppliesKMS: checks puts.
func testS3SinkKeysByTemplateAndAppliesKMSChecksPuts(
	t *testing.T,
	api *mocks.MockOutboxS3API,
	recs []types.OutboxRecord,
) {
	t.Helper()

	puts := expectS3Puts(t, api)

	cfg := outbox.DefaultConfig()

	cfg.Kind = outbox.KindS3
	cfg.S3 = outboxs3.Config{
		Bucket:      textArchive,
		KeyTemplate: "audit/{tenant}/{yyyy}/{mm}/{dd}/{id}.json",
	}

	sink, err := outbox.NewFromConfig(
		t.Context(),
		&cfg,
		outbox.Deps{S3API: api, Decorations: decorate.Decorations{}, SQSAPI: nil},
	)
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	assert.Equal(t, []apperr.ErrorCode{"", "", apperr.CodeForbidden}, codesOf(results))
	require.Len(t, *puts, wantPutsLen)

	for i, p := range *puts {
		assertS3Put(t, &recs[i], p)
	}

	assert.Equal(t, s3types.ServerSideEncryption(""), (*puts)[0].in.ServerSideEncryption)
	assert.Nil(t, (*puts)[0].in.SSEKMSKeyId)
	assert.Equal(
		t,
		s3types.ServerSideEncryptionAwsKms,
		(*puts)[1].in.ServerSideEncryption,
	)
	assert.Equal(
		t,
		"arn:aws:kms:us-east-1:000000000000:key/k-1",
		aws.ToString((*puts)[1].in.SSEKMSKeyId),
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
	recs := sinkRecords(sinkRecordsInput)

	var bodies []string

	testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyExpectsPuts(
		t,
		api,
		recs,
		bodies,
	)
}

// testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyExpectsPuts continues
// testOutboxSinkDecoratorsRetryOnlyTransientWithFullBody: expects puts.
func testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyExpectsPuts(
	t *testing.T,
	api *mocks.MockOutboxS3API,
	recs []types.OutboxRecord,
	bodies []string,
) {
	t.Helper()

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
					Code:    "InternalError",
					Fault:   smithy.FaultServer,
					Message: "",
				}
			}),
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				in *awss3.PutObjectInput,
				_ ...func(*awss3.Options),
			) (*awss3.PutObjectOutput, error) {
				read(in)

				return new(awss3.PutObjectOutput), nil
			}),
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
			Return(
				nil, &smithy.GenericAPIError{
					Code: "InvalidArgument", Fault: smithy.FaultClient,
					Message: "",
				},
			).
			Times(1),
	)

	testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyChecksResults(
		t,
		api,
		recs,
		&bodies,
	)
}

// testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyChecksResults continues
// testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyExpectsPuts: checks results.
func testOutboxSinkDecoratorsRetryOnlyTransientWithFullBodyChecksResults(
	t *testing.T,
	api *mocks.MockOutboxS3API,
	recs []types.OutboxRecord,
	bodies *[]string,
) {
	t.Helper()

	cfg := outbox.DefaultConfig()

	cfg.Kind, cfg.S3.Bucket = outbox.KindS3, textArchive

	enabled := true
	decs := decoration.DefaultConfig()

	decs.Clients.Overrides["outbox.s3"] = decoration.Merge(
		new(decs.Clients.Overrides["outbox.s3"]),
		fixtures.BuildRef(func(row *decoration.Layers) {
			row.Retry = decoration.Retry{
				Enabled: &enabled,
				RetryConfig: resilience.RetryConfig{
					MaxRetries:      3,
					InitialInterval: time.Millisecond,
					MaxInterval:     time.Millisecond,
					MaxElapsedTime:  0,
					Multiplier:      0,
				},
			}
		}),
	)

	sink, err := outbox.NewFromConfig(t.Context(), &cfg, outbox.Deps{
		Decorations: decorate.NewDecorations(&decs, stdx.Zero[decorate.Collaborators]()),
		S3API:       api,
		SQSAPI:      nil,
	})
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	assert.Equal(t, []apperr.ErrorCode{"", apperr.CodeInvalidInput}, codesOf(results))
	assert.Equal(t, []string{string(recs[0].Payload), string(recs[0].Payload)}, (*bodies))
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
	recs := sinkRecords(sinkRecordsInputIDs)

	recs[0].Key = textDoc7
	recs[0].Attributes = map[string]string{types.OutboxRouteAttribute: textOrdered}
	recs[1].Attributes = map[string]string{types.OutboxRouteAttribute: textOrdered}

	expectTwoQueueURLs(api)

	sent := map[string][]sqstypes.SendMessageBatchRequestEntry{}

	expectTwoQueueSends(api, sent)

	sink, err := outboxsqs.New(outboxsqs.Config{
		Queue: textPlain, Routes: map[string]string{textOrdered: "events.fifo"},
		FairQueues: nil,
	}, api)
	require.NoError(t, err)

	assert.Equal(t, []error{nil, nil, nil}, sink.Send(context.Background(), recs))

	fifo := sent["u-events.fifo"]
	require.Len(t, fifo, wantFifoLen)
	assert.Equal(t, textDoc7, aws.ToString(fifo[0].MessageGroupId))
	assert.Equal(t, recs[0].ID.String(), aws.ToString(fifo[0].MessageDeduplicationId))
	assert.Equal(t, textOrg1, aws.ToString(fifo[1].MessageGroupId))
	assert.Equal(t, recs[1].ID.String(), aws.ToString(fifo[1].MessageDeduplicationId))

	plain := sent["u-plain"]
	require.Len(t, plain, 1)
	assert.Nil(t, plain[0].MessageGroupId)
	assert.Nil(t, plain[0].MessageDeduplicationId)
}

// TestSQSSink_FairQueueCarriesAGroupButNoDedupID tests delivery to an SQS fair queue.
//
// Why this test is important:
//   - A standard queue gives every tenant one shared backlog, so one tenant's burst
//     delays everyone else's messages. SQS fair queues reorder deliveries by message
//     group, which needs a MessageGroupId on each message of a standard queue.
//
// What it tests:
//   - an entry for a queue listed in FairQueues carries MessageGroupId = the record's
//     Key (else its tenant) and no deduplication id, which a standard queue rejects
//   - an entry for a standard queue not listed carries neither
func TestSQSSink_FairQueueCarriesAGroupButNoDedupID(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(sinkRecordsInputID)

	recs[0].Key = "sync-7"
	recs[0].Attributes = map[string]string{types.OutboxRouteAttribute: textBulk}
	recs[1].Attributes = map[string]string{types.OutboxRouteAttribute: textBulk}

	expectTwoQueueURLs(api)

	sent := map[string][]sqstypes.SendMessageBatchRequestEntry{}

	expectTwoQueueSends(api, sent)

	sink, err := outboxsqs.New(outboxsqs.Config{
		Queue:      textPlain,
		Routes:     map[string]string{textBulk: "batches"},
		FairQueues: []string{"batches"},
	}, api)
	require.NoError(t, err)

	assert.Equal(t, []error{nil, nil, nil}, sink.Send(context.Background(), recs))

	fair := sent["u-batches"]
	require.Len(t, fair, wantFairLen)
	assert.Equal(t, "sync-7", aws.ToString(fair[0].MessageGroupId))
	assert.Equal(t, textOrg1, aws.ToString(fair[1].MessageGroupId))
	assert.Nil(t, fair[0].MessageDeduplicationId)

	plain := sent["u-plain"]
	require.Len(t, plain, 1)
	assert.Nil(t, plain[0].MessageGroupId)
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

	testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches(t)
}

// testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches continues
// testSQSSinkPacksBatchesWithinSQSByteLimits: checks batches.
func testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches(t *testing.T) {
	t.Helper()

	const (
		maxRequestBytes = 1 << 20
		maxMessageBytes = 256 << 10
	)

	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(sinkRecordsInputBatches)

	for i := range recs[:offset] {
		recs[i].Payload = []byte(strings.Repeat(textA, 200<<10))
	}

	recs[index].Payload = []byte(strings.Repeat(textB, maxMessageBytes+1))

	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{
			QueueUrl:       aws.String(textU),
			ResultMetadata: middleware.Metadata{},
		}, nil)

	requests := captureSendBatches(api)

	sink, err := outboxsqs.New(
		outboxsqs.Config{Queue: textQ, Routes: nil, FairQueues: nil},
		api,
	)
	require.NoError(t, err)

	results := sink.Send(context.Background(), recs)

	want := make([]apperr.ErrorCode, len(recs))

	want[index] = apperr.CodeInvalidInput
	assert.Equal(t, want, codesOf(results))
	require.Greater(t, len(*requests), 1)

	sent := 0

	for _, entries := range *requests {
		assert.LessOrEqual(t, sqsRequestBytes(entries), maxRequestBytes)

		sent += len(entries)
	}

	assert.Equal(t, wantSent, sent, "every sendable record sent exactly once")
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
		Return(&awssqs.GetQueueUrlOutput{
			QueueUrl:       aws.String(textU),
			ResultMetadata: middleware.Metadata{},
		}, nil)

	var sent *awssqs.SendMessageBatchInput

	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			sent = in

			return &awssqs.SendMessageBatchOutput{
				Successful: []sqstypes.SendMessageBatchResultEntry{
					fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
						row.Id = aws.String(text0)
					}),
				},
				Failed:         nil,
				ResultMetadata: middleware.Metadata{},
			}, nil
		})

	sink, err := outboxsqs.New(
		outboxsqs.Config{Queue: textQ, Routes: nil, FairQueues: nil},
		api,
	)
	require.NoError(t, err)

	assert.Equal(t, []error{nil}, sink.Send(context.Background(), recs))
	require.NotNil(t, sent)
	assert.Equal(t, map[string]sqstypes.MessageAttributeValue{
		"outbox_id": fixtures.Build(func(row *sqstypes.MessageAttributeValue) {
			row.DataType = aws.String("String")
			row.StringValue = aws.String(recs[0].ID.String())
		}),
		textLane: fixtures.Build(func(row *sqstypes.MessageAttributeValue) {
			row.DataType = aws.String("String")
			row.StringValue = aws.String(textAudit)
		}),
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

	recs[0].Key = textDoc7

	var key string

	api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			in *awss3.PutObjectInput,
			_ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			key = aws.ToString(in.Key)

			return new(awss3.PutObjectOutput), nil
		})

	sink, err := outboxs3.New(
		outboxs3.Config{Bucket: textArchive, KeyTemplate: "{tenant}/{key}/{id}"},
		api,
	)
	require.NoError(t, err)

	assert.Equal(t, []error{nil}, sink.Send(context.Background(), recs))
	assert.Equal(t, "org-1/doc-7/"+recs[0].ID.String(), key)
}

// TestSQSSink_SendsTheKeyAsPartitionKey tests the sink's partition_key attribute.
//
// Why this test is important:
//   - A consumer groups a document's events by key, and a later move to a
//     partitioned broker routes by it; the relay must carry the record's key
//     unchanged, and a record with no key must look exactly as before
//
// What it tests:
//   - a record with Key "doc-7" is sent with a partition_key String attribute of
//     "doc-7"; a record with no Key gets no partition_key attribute
func TestSQSSink_SendsTheKeyAsPartitionKey(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockOutboxSQSAPI(ctrl)
	recs := sinkRecords(sinkRecordsInput)

	recs[0].Key = textDoc7

	var sent *awssqs.SendMessageBatchInput

	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{
			QueueUrl:       aws.String("http://q"),
			ResultMetadata: middleware.Metadata{},
		}, nil)
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			sent = in

			return &awssqs.SendMessageBatchOutput{
				Successful: []sqstypes.SendMessageBatchResultEntry{
					fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
						row.Id = aws.String(text0)
					}),
					fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
						row.Id = aws.String("1")
					}),
				},
				Failed:         nil,
				ResultMetadata: middleware.Metadata{},
			}, nil
		})

	testSQSSinkSendsTheKeyAsPartitionKeyChecksRequest(t, api, recs, &sent)
}

// testSQSSinkSendsTheKeyAsPartitionKeyChecksRequest continues
// TestSQSSink_SendsTheKeyAsPartitionKey.
func testSQSSinkSendsTheKeyAsPartitionKeyChecksRequest(
	t *testing.T,
	api *mocks.MockOutboxSQSAPI,
	recs []types.OutboxRecord,
	sent **awssqs.SendMessageBatchInput,
) {
	t.Helper()

	cfg := outbox.DefaultConfig()

	cfg.Kind, cfg.SQS = outbox.KindSQS, outboxsqs.Config{
		Queue:      "events",
		Routes:     nil,
		FairQueues: nil,
	}

	sink, err := outbox.NewFromConfig(
		t.Context(),
		&cfg,
		outbox.Deps{SQSAPI: api, Decorations: decorate.Decorations{}, S3API: nil},
	)
	require.NoError(t, err)

	require.Equal(
		t,
		[]apperr.ErrorCode{"", ""},
		codesOf(sink.Send(context.Background(), recs)),
	)

	require.NotNil(t, (*sent))

	key := (*sent).Entries[0].MessageAttributes[interfaces.PartitionKeyAttribute]
	assert.Equal(t, textDoc7, aws.ToString(key.StringValue))
	assert.Equal(t, "String", aws.ToString(key.DataType))
	assert.NotContains(
		t,
		(*sent).Entries[1].MessageAttributes,
		interfaces.PartitionKeyAttribute,
	)
}

// expectTwoQueueSends accepts two batches, recording each queue's entries in sent.
func expectTwoQueueSends(
	api *mocks.MockOutboxSQSAPI,
	sent map[string][]sqstypes.SendMessageBatchRequestEntry,
) {
	var lock sync.Mutex

	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).Times(2).
		DoAndReturn(func(
			_ context.Context,
			sendMessageBatchInput *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			out := new(awssqs.SendMessageBatchOutput)

			lock.Lock()
			defer lock.Unlock()

			sent[aws.ToString(sendMessageBatchInput.QueueUrl)] = sendMessageBatchInput.Entries
			for _, e := range sendMessageBatchInput.Entries {
				out.Successful = append(
					out.Successful,
					fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
						row.Id = e.Id
					}),
				)
			}

			return out, nil
		})
}

// expectTwoQueueURLs resolves two queue names to "u-<name>" URLs.
func expectTwoQueueURLs(api *mocks.MockOutboxSQSAPI) {
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).Times(2).
		DoAndReturn(func(
			_ context.Context,
			in *awssqs.GetQueueUrlInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.GetQueueUrlOutput, error) {
			return &awssqs.GetQueueUrlOutput{
				QueueUrl:       aws.String("u-" + aws.ToString(in.QueueName)),
				ResultMetadata: middleware.Metadata{},
			}, nil
		})
}

// s3Put is one captured PutObject call.
type s3Put struct {
	in   *awss3.PutObjectInput
	body string
}

// expectS3Puts captures three puts, the last refused with AccessDenied.
func expectS3Puts(t *testing.T, api *mocks.MockOutboxS3API) *[]s3Put {
	t.Helper()

	puts := &[]s3Put{}

	api.EXPECT().PutObject(gomock.Any(), gomock.Any()).Times(3).
		DoAndReturn(func(
			_ context.Context,
			putObjectInput *awss3.PutObjectInput,
			_ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			body, err := io.ReadAll(putObjectInput.Body)
			require.NoError(t, err)

			*puts = append(*puts, s3Put{in: putObjectInput, body: string(body)})
			if len(*puts) == 3 {
				return nil, &smithy.GenericAPIError{
					Code:    textAccessdenied,
					Fault:   smithy.FaultClient,
					Message: "",
				}
			}

			return new(awss3.PutObjectOutput), nil
		})

	return puts
}

// assertS3Put pins the bucket, key template, body and checksum of one put.
func assertS3Put(t *testing.T, rec *types.OutboxRecord, currentS3Put s3Put) {
	t.Helper()

	sum := sha256.Sum256(rec.Payload)

	assert.Equal(t, textArchive, aws.ToString(currentS3Put.in.Bucket))
	assert.Equal(
		t,
		"audit/org-1/2026/10/10/"+rec.ID.String()+".json",
		aws.ToString(currentS3Put.in.Key),
	)
	assert.Equal(t, string(rec.Payload), currentS3Put.body)
	assert.Equal(
		t,
		base64.StdEncoding.EncodeToString(sum[:]),
		aws.ToString(currentS3Put.in.ChecksumSHA256),
	)
	assert.Equal(t, s3types.ChecksumAlgorithmSha256, currentS3Put.in.ChecksumAlgorithm)
	assert.Nil(t, currentS3Put.in.ContentMD5)
}

// captureSendBatches accepts every batch and records each request's entries.
func captureSendBatches(
	api *mocks.MockOutboxSQSAPI,
) *[][]sqstypes.SendMessageBatchRequestEntry {
	requests := &[][]sqstypes.SendMessageBatchRequestEntry{}

	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(
			_ context.Context,
			sendMessageBatchInput *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			*requests = append(*requests, sendMessageBatchInput.Entries)

			out := new(awssqs.SendMessageBatchOutput)

			for _, e := range sendMessageBatchInput.Entries {
				out.Successful = append(
					out.Successful,
					fixtures.Build(func(row *sqstypes.SendMessageBatchResultEntry) {
						row.Id = e.Id
					}),
				)
			}

			return out, nil
		})

	return requests
}

// sqsRequestBytes totals the bodies and attributes SQS counts against a request.
func sqsRequestBytes(entries []sqstypes.SendMessageBatchRequestEntry) int {
	size := 0

	for _, e := range entries {
		size += len(aws.ToString(e.MessageBody))
		for name, v := range e.MessageAttributes {
			size += len(name) + len(aws.ToString(v.DataType)) +
				len(aws.ToString(v.StringValue))
		}
	}

	return size
}

const (
	// dateInputRecords is the month passed to Date in sinkRecords.
	dateInputRecords = 10

	// dateInputSinkRecords is the day passed to Date in sinkRecords.
	dateInputSinkRecords = 9

	// sinkRecordsHour is the hour passed to Date in sinkRecords.
	sinkRecordsHour = 23

	// sinkRecordsMinute is the min passed to Date in sinkRecords.
	sinkRecordsMinute = 30

	// index is an index into recs in
	// testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches, and an index into want in
	// testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches.
	index = 10

	// makeInputRecords is argument 3 of make in
	// TestSQSSink_MapsPartialBatchFailureToRecords.
	makeInputRecords = 8

	// offset is a slice bound of recs in
	// testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches.
	offset = 10

	// sinkRecordsInput is the n passed to sinkRecords in
	// TestOutboxSinkDecorators_RetryOnlyTransientWithFullBody,
	// TestOutboxSinkFromConfig_DefaultsToStubAndRejectsIncompleteBackends and 1 more.
	sinkRecordsInput = 2

	// sinkRecordsInputBatches is the n passed to sinkRecords in
	// testSQSSinkPacksBatchesWithinSQSByteLimitsChecksBatches.
	sinkRecordsInputBatches = 11

	// sinkRecordsInputID is the n passed to sinkRecords in
	// TestSQSSink_FairQueueCarriesAGroupButNoDedupID.
	sinkRecordsInputID = 3

	// sinkRecordsInputIDs is the n passed to sinkRecords in
	// TestSQSSink_FIFOQueueCarriesGroupAndDedupIDs.
	sinkRecordsInputIDs = 3

	// sinkRecordsInputKMS is the n passed to sinkRecords in
	// TestS3Sink_KeysByTemplateAndAppliesKMS.
	sinkRecordsInputKMS = 3

	// sinkRecordsInputQueue is the n passed to sinkRecords in
	// TestSQSSink_RoutesByKeyAndIsolatesFailingQueue.
	sinkRecordsInputQueue = 6

	// sinkRecordsInputRecords is the n passed to sinkRecords in
	// TestSQSSink_MapsPartialBatchFailureToRecords.
	sinkRecordsInputRecords = 12

	// wantEntriesLen is the expected entries len.
	wantEntriesLen = 10

	// wantFairLen is the expected fair len.
	wantFairLen = 2

	// wantFifoLen is the expected fifo len.
	wantFifoLen = 2

	// wantPutsLen is the expected puts len.
	wantPutsLen = 3

	// wantSent is the expected sent.
	wantSent = 10
)

const (
	// textArchive is the literal "archive".
	textArchive = "archive"

	// textBulk is the literal "bulk".
	textBulk = "bulk"

	// textDoc7 is the literal "doc-7".
	textDoc7 = "doc-7"

	// textOrdered is the literal "ordered".
	textOrdered = "ordered"

	// textStandard is the literal "standard".
	textStandard = "standard"
)
