package unit_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/mock/gomock"

	storagedecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// fastStorageRetrier builds a real Retrier with tiny intervals for retry-safety tests.
func fastStorageRetrier(t *testing.T) interfaces.Retrier {
	t.Helper()
	r, err := retry.NewFromConfig(retry.Config{
		Kind: retry.KindExponential, MaxRetries: 3,
		InitialInterval: time.Millisecond, MaxInterval: 2 * time.Millisecond,
		Multiplier: 2.0, MaxElapsedTime: time.Second,
	})
	require.NoError(t, err)
	return r
}

// TestStorageDecorator_RetryClassification tests that the per-op Retryable flags
// are wired correctly: an idempotent read (Delete) is retried while a
// body-consuming op (Upload) is never replayed, even with the Retry layer enabled.
//
// Why this test is important:
//   - Replaying a body-consuming Upload corrupts data; the classification is the
//     guard, and a regression flipping Upload to retryable would be silent.
//
// What it tests:
//   - With a Retrier enabled, a failing Upload is invoked exactly once, while a
//     Delete that fails once then succeeds is invoked twice.
func TestStorageDecorator_RetryClassification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Upload is non-retryable: one call even though it fails.
	ctrl := gomock.NewController(t)
	base := mocks.NewMockStorageClient(ctrl)
	base.EXPECT().Upload(gomock.Any(), "b", "k", gomock.Any(), "").
		Return(apperr.Sentinel("boom")).Times(1)
	up := storagedecorators.NewBuilder(base, "docs").
		WithRetrier(fastStorageRetrier(t)).Build()
	require.Error(t, up.Upload(ctx, "b", "k", strings.NewReader("x"), ""))

	// Delete is retryable: fails once, retried, then succeeds.
	base2 := mocks.NewMockStorageClient(ctrl)
	gomock.InOrder(
		base2.EXPECT().Delete(gomock.Any(), "b", "k").Return(apperr.Sentinel("boom")),
		base2.EXPECT().Delete(gomock.Any(), "b", "k").Return(nil),
	)
	del := storagedecorators.NewBuilder(base2, "docs").
		WithRetrier(fastStorageRetrier(t)).Build()
	require.NoError(t, del.Delete(ctx, "b", "k"))
}

// TestStorageDecorator_CircuitBreakerOpen_FailsFast verifies an open breaker
// short-circuits before the wrapped client is touched.
//
// Why this test is important:
//   - When S3 is failing the breaker must stop hammering it; if the decorator still
//     calls the base, the breaker provides no protection
//
// What it tests:
//   - With an open breaker, the op errors and the base StorageClient is never called
//     (the gomock base has no expectations, so any call fails the test)
func TestStorageDecorator_CircuitBreakerOpen_FailsFast(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockStorageClient(ctrl) // no EXPECT → must not be called
	client := storagedecorators.NewBuilder(base, "docs").
		WithCircuitBreaker(fixtures.StubCircuitBreaker(true)).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	_, err := client.Download(context.Background(), "b", "k")
	require.Error(t, err)
}

// TestStorageDecorator_DownloadStreamSurvivesTimeout verifies the per-op timeout
// does not cancel the download body before the caller reads it.
//
// Why this test is important:
//   - Download returns a lazy stream read AFTER the method returns; cancelling the
//     timeout context on return aborts the read with "context canceled" — a real bug
//
// What it tests:
//   - With a timeout set, the returned stream reads fully (context still alive)
func TestStorageDecorator_DownloadStreamSurvivesTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockStorageClient(ctrl)

	// The body is bound to the context the decorator passes to Download, so a
	// decorator that wrongly cancels the timeout context on return surfaces here (the
	// read below happens AFTER Download returned).
	base.EXPECT().Download(gomock.Any(), "b", "k").DoAndReturn(
		func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			return ctxBoundBody(ctx, ctrl, []byte("streamed payload")), nil
		},
	)

	client := storagedecorators.NewBuilder(base, "docs").
		WithTimeout(5 * time.Second).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	rc, err := client.Download(context.Background(), "b", "k")
	require.NoError(t, err)
	data, err := io.ReadAll(rc) // reads AFTER Download returned — must not be cancelled
	require.NoError(t, err)
	assert.Equal(t, "streamed payload", string(data))
	require.NoError(t, rc.Close())
}

// ctxBoundBody returns a generated ReadCloser mock that models an S3 GetObject body
// tied to the call context: each Read surfaces the context error once ctx is
// cancelled, streams data otherwise, then EOFs. Close succeeds once.
func ctxBoundBody(
	ctx context.Context,
	ctrl *gomock.Controller,
	data []byte,
) io.ReadCloser {
	var pos int
	body := mocks.NewMockReadCloser(ctrl)
	body.EXPECT().Read(gomock.Any()).DoAndReturn(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, apperr.Wrap(err, apperr.CodeCanceled, "stream read")
		}
		if pos >= len(data) {
			return 0, io.EOF
		}
		n := copy(p, data[pos:])
		pos += n
		return n, nil
	}).AnyTimes()
	body.EXPECT().Close().Return(nil)
	return body
}

// TestStorageDecorator_LogsAndCountsOnError verifies failed operations are logged
// and the metrics path is exercised.
//
// Why this test is important:
//   - Structured failure logs are how operators diagnose storage failures; a silent
//     decorator hides the failure context. As an INNER seam the client stack logs the
//     failure at Debug (suppressed in staging/prod); only the outermost seam logs Error.
//
// What it tests:
//   - A base error triggers a Debug failure log (and the metrics path runs via
//     NopMetrics)
func TestStorageDecorator_LogsAndCountsOnError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockStorageClient(ctrl)
	base.EXPECT().
		Upload(gomock.Any(), "b", "k", gomock.Any(), gomock.Any()).
		Return(apperr.Sentinel("boom"))

	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().
		Debug(
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
		).
		MinTimes(1)

	client := storagedecorators.NewBuilder(base, "docs").
		WithLogger(logger).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	err := client.Upload(
		context.Background(),
		"b",
		"k",
		strings.NewReader("x"),
		"text/plain",
	)
	require.Error(t, err)
}

// TestStorageDecorator_DelegatesToBase verifies every instrumented pass-through
// method forwards to the base client and returns its result unchanged.
//
// Why this test is important:
//   - The decorator must be transparent: wrapping a StorageClient for
//     metrics/tracing/timeout must not alter delete/exists/stat/presign/list
//     semantics. A wrapper that drops an argument or swallows a return value is a
//     silent data-loss bug that only surfaces in production.
//
// What it tests:
//   - Delete, DeleteBatch, Exists, Stat, PresignURL, PresignPutURL, ListObjects,
//     EnsureBucket, and the multipart surface (CreateMultipartUpload,
//     PresignUploadPart, CompleteMultipartUpload, AbortMultipartUpload,
//     ListMultipartUploads) each call the matching base method with the same
//     arguments and propagate its return value, driven through the full
//     metrics + tracer + timeout stack.
func TestStorageDecorator_DelegatesToBase(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockStorageClient(ctrl)

	obj := interfaces.StorageObject{Key: "k", ContentType: "text/plain", Size: 3}
	keys := []string{"a", "b"}
	parts := []interfaces.CompletedPart{{PartNumber: 1, ETag: "etag-1"}}
	uploads := []interfaces.MultipartUpload{
		{Key: "k", UploadID: "upload-1", Initiated: "2026-07-08T00:00:00Z"},
	}

	base.EXPECT().Delete(gomock.Any(), "b", "k").Return(nil)
	base.EXPECT().DeleteBatch(gomock.Any(), "b", keys).Return(nil)
	base.EXPECT().Exists(gomock.Any(), "b", "k").Return(true, nil)
	base.EXPECT().Stat(gomock.Any(), "b", "k").Return(obj, nil)
	base.EXPECT().
		PresignURL(gomock.Any(), "b", "k", 900, "doc.txt").
		Return("https://get", nil)
	base.EXPECT().
		PresignPutURL(gomock.Any(), "b", "k", 900, "text/plain").
		Return("https://put", nil)
	base.EXPECT().
		ListObjects(gomock.Any(), "b", "prefix/").
		Return([]interfaces.StorageObject{obj}, nil)
	base.EXPECT().EnsureBucket(gomock.Any(), "b").Return(nil)
	base.EXPECT().
		CreateMultipartUpload(gomock.Any(), "b", "k", "text/plain").
		Return("upload-1", nil)
	base.EXPECT().
		PresignUploadPart(gomock.Any(), "b", "k", "upload-1", int32(1), 900).
		Return("https://part", nil)
	base.EXPECT().
		CompleteMultipartUpload(gomock.Any(), "b", "k", "upload-1", parts).
		Return(nil)
	base.EXPECT().AbortMultipartUpload(gomock.Any(), "b", "k", "upload-1").Return(nil)
	base.EXPECT().ListMultipartUploads(gomock.Any(), "b").Return(uploads, nil)

	// A tracer + timeout exercise the span and deadline branches of do() that the
	// Download-only tests above never reach.
	client := storagedecorators.NewBuilder(base, "docs").
		WithMetrics(fixtures.NopMetrics()).
		WithTracer(noop.NewTracerProvider().Tracer("test")).
		WithTimeout(5 * time.Second).
		Build()

	ctx := context.Background()

	require.NoError(t, client.Delete(ctx, "b", "k"))
	require.NoError(t, client.DeleteBatch(ctx, "b", keys))

	exists, err := client.Exists(ctx, "b", "k")
	require.NoError(t, err)
	assert.True(t, exists)

	got, err := client.Stat(ctx, "b", "k")
	require.NoError(t, err)
	assert.Equal(t, obj, got)

	getURL, err := client.PresignURL(ctx, "b", "k", 900, "doc.txt")
	require.NoError(t, err)
	assert.Equal(t, "https://get", getURL)

	putURL, err := client.PresignPutURL(ctx, "b", "k", 900, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "https://put", putURL)

	list, err := client.ListObjects(ctx, "b", "prefix/")
	require.NoError(t, err)
	assert.Equal(t, []interfaces.StorageObject{obj}, list)

	require.NoError(t, client.EnsureBucket(ctx, "b"))

	uploadID, err := client.CreateMultipartUpload(ctx, "b", "k", "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "upload-1", uploadID)

	partURL, err := client.PresignUploadPart(ctx, "b", "k", "upload-1", 1, 900)
	require.NoError(t, err)
	assert.Equal(t, "https://part", partURL)

	require.NoError(t, client.CompleteMultipartUpload(ctx, "b", "k", "upload-1", parts))
	require.NoError(t, client.AbortMultipartUpload(ctx, "b", "k", "upload-1"))

	gotUploads, err := client.ListMultipartUploads(ctx, "b")
	require.NoError(t, err)
	assert.Equal(t, uploads, gotUploads)
}
