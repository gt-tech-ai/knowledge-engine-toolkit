package unit_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	connector "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	connectors3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// stubBuilder returns an S3ClientBuilder that hands back sc and (optionally) passes the
// creds it was given to capture, so a test can assert the auth strategy actually
// produced a credentials provider.
func stubBuilder(
	sc interfaces.StorageClient,
	capture func(aws.CredentialsProvider),
) connectors3.S3ClientBuilder {
	return func(
		_ context.Context,
		creds aws.CredentialsProvider,
		_ string,
	) (interfaces.StorageClient, error) {
		if capture != nil {
			capture(creds)
		}
		return sc, nil
	}
}

func s3Cfg(method string) interfaces.SourceConfig {
	return interfaces.SourceConfig{
		Kind:            interfaces.ConnectorKindS3,
		Bucket:          "b",
		Prefix:          "docs/",
		Region:          "us-east-1",
		AuthMethod:      method,
		AccessKeyID:     "AK",
		SecretAccessKey: "SK",
		IAMRoleARN:      "arn:aws:iam::1:role/r",
		ExternalID:      "ext-1",
	}
}

// TestS3ConnectorSource_TestConnection tests the S3 ConnectorSource probe over a mocked
// StorageClient: it counts objects under the bucket/prefix and classifies failures.
//
// Why this test is important:
//   - TestConnection is the connector's reachability probe; it must reuse the shared
//     StorageClient (bounded ListObjectsPage) and return a TRUTHFUL coded error — a bad
//     bucket/credentials (client fault) is a configuration problem (InvalidInput), a
//     transport/service failure is transient (Unavailable) — so the caller shows the
//     right message and retries only what can succeed.
//
// What it tests:
//   - a successful bounded list returns the object count; a client-fault AWS API error →
//     CodeInvalidInput; a transport (non-API) error → CodeUnavailable.
func TestS3ConnectorSource_TestConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("counts objects under the prefix", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().ListObjectsPage(gomock.Any(), "b", "docs/", gomock.Any()).
			Return([]interfaces.StorageObject{{Key: "docs/a"}, {Key: "docs/b"}}, nil)

		src, err := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		require.NoError(t, err)
		n, err := src.TestConnection(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, n)
	})

	t.Run("a client-fault API error → InvalidInput", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPage(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, &smithy.GenericAPIError{Code: "AccessDenied", Fault: smithy.FaultClient})

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, err := src.TestConnection(ctx)
		require.True(
			t,
			apperr.Is(err, apperr.CodeInvalidInput),
			"bad bucket/creds is the user's problem",
		)
	})

	t.Run("a transport error → Unavailable", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPage(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, apperr.Sentinel("dial tcp: connection refused"))

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, err := src.TestConnection(ctx)
		require.True(
			t,
			apperr.Is(err, apperr.CodeUnavailable),
			"a transport failure is transient",
		)
	})
}

// TestS3ConnectorSource_ListPage tests the streaming crawl a caller pages a large bucket
// with: it threads the caller's continuation token through, surfaces the resume token,
// and classifies a failure (transient vs terminal) so the caller can decide
// retry-vs-fail.
//
// Why this test is important:
//   - A caller crawls a million-object bucket through this seam and checkpoints the
//     returned token to crash-resume; if the token weren't passed through the crawl would
//     re-list from the start (infinite loop / double work), and a mis-classified error
//     would either retry a permanent failure forever or give up on a transient one
//     mid-crawl.
//
// What it tests:
//   - the caller's token reaches the StorageClient and the resume token is returned; a
//     client-fault AWS error → CodeInvalidInput (terminal); a transport error →
//     CodeUnavailable (transient).
func TestS3ConnectorSource_ListPage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("streams a page and returns the resume token", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPageToken(gomock.Any(), "b", "docs/", "tok1", 1000).
			Return([]interfaces.StorageObject{{Key: "docs/a"}}, "tok2", nil)

		src, err := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		require.NoError(t, err)
		objs, next, err := src.ListPage(ctx, "tok1", 1000)
		require.NoError(t, err)
		require.Len(t, objs, 1)
		require.Equal(t, "tok2", next, "the resume token is surfaced for checkpointing")
	})

	t.Run("a client-fault API error → InvalidInput (terminal)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPageToken(
				gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			).
			Return(
				nil, "", &smithy.GenericAPIError{Code: "NoSuchBucket", Fault: smithy.FaultClient},
			)

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, _, err := src.ListPage(ctx, "", 1000)
		require.True(t, apperr.Is(err, apperr.CodeInvalidInput))
	})

	t.Run("a transport error → Unavailable (transient)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPageToken(
				gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			).
			Return(nil, "", apperr.Sentinel("dial tcp: connection refused"))

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, _, err := src.ListPage(ctx, "", 1000)
		require.True(t, apperr.Is(err, apperr.CodeUnavailable))
	})
}

// TestS3ConnectorSource_AuthSelection tests the credential-strategy selection: an unknown
// method fails loud, and each known method yields a credentials provider handed to the
// client builder.
//
// Why this test is important:
//   - The auth strategy is the S3-specific seam (STS AssumeRole vs static key); a wrong
//     or silent selection would sign requests with the wrong (or no) credentials, and an
//     unknown method must fail at build time, not as a cryptic S3 error later.
//
// What it tests:
//   - an unknown auth_method → CodeInvalidInput at NewSource; access_key and iam_role
//     each produce a non-nil credentials provider passed to the injected builder, and the
//     access_key provider signs with the configured key id and secret (not a neighbouring
//     field such as the external id).
func TestS3ConnectorSource_AuthSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	sc := mocks.NewMockStorageClient(ctrl)

	_, err := connectors3.NewSource(ctx, s3Cfg("bogus"), nil, stubBuilder(sc, nil))
	require.True(
		t,
		apperr.Is(err, apperr.CodeInvalidInput),
		"unknown auth method fails loud",
	)

	for _, method := range []string{"access_key", "iam_role"} {
		var captured aws.CredentialsProvider
		_, err := connectors3.NewSource(
			ctx,
			s3Cfg(method),
			mocks.NewMockAssumeRoleAPIClient(ctrl),
			stubBuilder(sc, func(c aws.CredentialsProvider) { captured = c }),
		)
		require.NoError(t, err, method)
		require.NotNil(
			t,
			captured,
			"the %s strategy yields a credentials provider",
			method,
		)
	}

	var static aws.CredentialsProvider
	_, err = connectors3.NewSource(
		ctx,
		s3Cfg("access_key"),
		nil,
		stubBuilder(sc, func(c aws.CredentialsProvider) { static = c }),
	)
	require.NoError(t, err)
	creds, err := static.Retrieve(ctx)
	require.NoError(t, err)
	require.Equal(t, "AK", creds.AccessKeyID)
	require.Equal(t, "SK", creds.SecretAccessKey)
}

// TestS3ConnectorSource_RejectsInvalidAuthConfig tests that NewSource (and the connector
// builder) refuse an auth config that cannot work, at build time and with
// CodeInvalidInput.
//
// Why this test is important:
//   - A config that cannot authenticate would otherwise build cleanly and fail later
//     inside the first S3 call — a nil STS client panics during credential retrieval, and
//     an empty role ARN or a malformed external id comes back from STS as an error that
//     looks like an outage. Failing at build names the bad field and never reaches AWS.
//
// What it tests:
//   - iam_role: an empty role ARN, a nil STS client, and an external id that is empty,
//     whitespace-only, too short, or outside AWS's allowed characters each fail NewSource
//     with CodeInvalidInput.
//   - access_key: an empty key id or secret fails NewSource with CodeInvalidInput.
//   - connector.NewSourceBuilder's Build surfaces the same refusal for an iam_role config
//     without an external id.
func TestS3ConnectorSource_RejectsInvalidAuthConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		mutate func(*interfaces.SourceConfig)
		name   string
		method string
		nilSTS bool
	}{
		{
			name:   "iam_role empty role ARN",
			method: "iam_role",
			mutate: func(c *interfaces.SourceConfig) {
				c.IAMRoleARN = ""
			},
		},
		{name: "iam_role nil STS client", method: "iam_role", nilSTS: true},
		{
			name:   "iam_role empty external id",
			method: "iam_role",
			mutate: func(c *interfaces.SourceConfig) {
				c.ExternalID = ""
			},
		},
		{
			name:   "iam_role whitespace external id",
			method: "iam_role",
			mutate: func(c *interfaces.SourceConfig) {
				c.ExternalID = "   "
			},
		},
		{
			name:   "iam_role one-char external id",
			method: "iam_role",
			mutate: func(c *interfaces.SourceConfig) {
				c.ExternalID = "x"
			},
		},
		{
			name:   "iam_role external id with a space",
			method: "iam_role",
			mutate: func(c *interfaces.SourceConfig) {
				c.ExternalID = "tenant 42"
			},
		},
		{
			name:   "access_key empty key id",
			method: "access_key",
			mutate: func(c *interfaces.SourceConfig) {
				c.AccessKeyID = ""
			},
		},
		{
			name:   "access_key empty secret",
			method: "access_key",
			mutate: func(c *interfaces.SourceConfig) {
				c.SecretAccessKey = ""
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			cfg := s3Cfg(tc.method)
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			var stsClient stscreds.AssumeRoleAPIClient
			if !tc.nilSTS {
				stsClient = mocks.NewMockAssumeRoleAPIClient(
					ctrl,
				) // no EXPECT: STS is never called
			}
			_, err := connectors3.NewSource(
				ctx,
				cfg,
				stsClient,
				stubBuilder(mocks.NewMockStorageClient(ctrl), nil),
			)
			require.True(
				t,
				apperr.Is(err, apperr.CodeInvalidInput),
				"%s: %v",
				tc.name,
				err,
			)
		})
	}

	t.Run("the connector builder surfaces the refusal", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cfg := s3Cfg("iam_role")
		cfg.ExternalID = ""
		b := connector.NewSourceBuilder(connector.Config{}, connector.Deps{
			BuildS3Client: stubBuilder(mocks.NewMockStorageClient(ctrl), nil),
			STS:           mocks.NewMockAssumeRoleAPIClient(ctrl),
		})
		_, err := b.Build(ctx, cfg)
		require.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%v", err)
	})
}

// TestS3ConnectorSource_ClassifiesCredentialAndAPIFailures tests how TestConnection
// classifies the two failure shapes the AWS SDK produces without a client fault: a
// credential-retrieval failure inside the first S3 call, and an unmodeled S3 error that
// only its HTTP status identifies.
//
// Why this test is important:
//   - The SDK resolves credentials lazily inside the S3 call, and STS's AccessDenied (a
//     wrong external id, or another tenant's role ARN) is unmodeled, so it reaches the
//     classifier with an unknown fault. Reported as "unreachable", a permanent
//     credentials problem is retried forever and the caller sees a misleading message.
//   - S3's own AccessDenied / InvalidAccessKeyId are unmodeled too; only the 4xx status
//     marks them as configuration errors, while 429 throttling must stay transient.
//
// What it tests:
//   - An STS AccessDenied during credential retrieval → CodeInvalidInput.
//   - A caller deadline during credential retrieval → CodeUnavailable (not a credentials
//     problem).
//   - An unmodeled 403 API error → CodeInvalidInput; an unmodeled 429 or 503 →
//     CodeUnavailable.
func TestS3ConnectorSource_ClassifiesCredentialAndAPIFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// listViaCredentials mimics the SDK: the S3 call retrieves the credentials first, and
	// a failure comes back wrapped in the S3 operation error, which the storage client
	// wraps again.
	listViaCredentials := func(captured *aws.CredentialsProvider) func(
		context.Context, string, string, int,
	) ([]interfaces.StorageObject, error) {
		return func(
			ctx context.Context,
			_,
			_ string,
			_ int,
		) ([]interfaces.StorageObject, error) {
			_, err := (*captured).Retrieve(ctx)
			require.Error(t, err)
			opErr := &smithy.OperationError{
				ServiceID:     "S3",
				OperationName: "ListObjectsV2",
				Err:           err,
			}
			return nil, apperr.Wrap(opErr, apperr.CodeInternal, "s3 list b/docs/")
		}
	}

	credCases := []struct {
		stsErr error
		name   string
		want   apperr.ErrorCode
	}{
		{
			name: "STS AccessDenied → InvalidInput",
			stsErr: &smithy.GenericAPIError{
				Code:    "AccessDenied",
				Message: "not authorized to assume",
			},
			want: apperr.CodeInvalidInput,
		},
		{
			name: "caller deadline → Unavailable",
			stsErr: &smithy.OperationError{
				ServiceID:     "STS",
				OperationName: "AssumeRole",
				Err:           context.DeadlineExceeded,
			},
			want: apperr.CodeUnavailable,
		},
	}
	for _, tc := range credCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			stsClient := mocks.NewMockAssumeRoleAPIClient(ctrl)
			stsClient.EXPECT().
				AssumeRole(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, tc.stsErr)
			var captured aws.CredentialsProvider
			sc := mocks.NewMockStorageClient(ctrl)
			sc.EXPECT().
				ListObjectsPage(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(listViaCredentials(&captured))

			src, err := connectors3.NewSource(
				ctx,
				s3Cfg("iam_role"),
				stsClient,
				stubBuilder(sc, func(c aws.CredentialsProvider) { captured = c }),
			)
			require.NoError(t, err)
			_, err = src.TestConnection(ctx)
			require.Equal(t, tc.want, apperr.Code(err), "%v", err)
		})
	}

	statusCases := []struct {
		name   string
		want   apperr.ErrorCode
		status int
	}{
		{
			name:   "unmodeled 403 → InvalidInput",
			status: http.StatusForbidden,
			want:   apperr.CodeInvalidInput,
		},
		{
			name:   "unmodeled 429 → Unavailable",
			status: http.StatusTooManyRequests,
			want:   apperr.CodeUnavailable,
		},
		{
			name:   "unmodeled 503 → Unavailable",
			status: http.StatusServiceUnavailable,
			want:   apperr.CodeUnavailable,
		},
	}
	for _, tc := range statusCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			sc := mocks.NewMockStorageClient(ctrl)
			apiErr := &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{
					Response: &http.Response{StatusCode: tc.status},
				},
				Err: &smithy.GenericAPIError{
					Code: "AccessDenied",
				}, // Fault unset, as S3 deserializes it
			}
			sc.EXPECT().
				ListObjectsPage(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, apperr.Wrap(apiErr, apperr.CodeInternal, "s3 list b/docs/"))

			src, err := connectors3.NewSource(
				ctx,
				s3Cfg("access_key"),
				nil,
				stubBuilder(sc, nil),
			)
			require.NoError(t, err)
			_, err = src.TestConnection(ctx)
			require.Equal(t, tc.want, apperr.Code(err), "%v", err)
		})
	}
}

// TestConnectorSourceBuilder_Build tests the factory: an unknown kind fails loud; the s3
// kind builds a decorated source that probes through the injected StorageClient.
//
// Why this test is important:
//   - The builder is the connector family's config-selected entry point; an unknown kind
//     must fail at build time (CodeInvalidInput), and the s3 kind must produce a working
//     ConnectorSource wired to the injected client builder + observability decorator — so
//     adding a kind is a config change, not an edit.
//
// What it tests:
//   - Build(kind="bogus") → CodeInvalidInput; Build(s3) → a source whose TestConnection
//     lists via the injected (mock) StorageClient and returns the count (the decorator
//     forwards with nil-obs deps).
func TestConnectorSourceBuilder_Build(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("unknown kind fails loud", func(t *testing.T) {
		t.Parallel()
		b := connector.NewSourceBuilder(connector.Config{}, connector.Deps{})
		_, err := b.Build(ctx, interfaces.SourceConfig{Kind: "bogus"})
		require.True(t, apperr.Is(err, apperr.CodeInvalidInput))
	})

	t.Run(
		"s3 kind builds a decorated source that probes via StorageClient",
		func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			sc := mocks.NewMockStorageClient(ctrl)
			sc.EXPECT().ListObjectsPage(gomock.Any(), "b", "docs/", gomock.Any()).
				Return([]interfaces.StorageObject{{Key: "docs/a"}}, nil)

			b := connector.NewSourceBuilder(connector.Config{}, connector.Deps{
				BuildS3Client: stubBuilder(sc, nil),
				STS:           mocks.NewMockAssumeRoleAPIClient(ctrl),
			})
			src, err := b.Build(ctx, s3Cfg("access_key"))
			require.NoError(t, err)
			n, err := src.TestConnection(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, n)
		},
	)
}

// TestS3ConnectorSource_IAMRoleSendsExternalID tests that the iam_role strategy always
// assumes the connector's role with the connector's ExternalId.
//
// Why this test is important:
//   - Without an ExternalId, a tenant who registers another tenant's role ARN gets the
//     platform to assume it for them (the confused-deputy problem); the ExternalId in the
//     role's trust policy is what ties the role to the tenant that owns it. (Refusing to
//     build without one is covered by TestS3ConnectorSource_RejectsInvalidAuthConfig.)
//
// What it tests:
//   - Retrieving the iam_role credentials calls STS AssumeRole with the connector's
//     RoleArn and ExternalId and returns the assumed credentials.
func TestS3ConnectorSource_IAMRoleSendsExternalID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	sc := mocks.NewMockStorageClient(ctrl)
	stsClient := mocks.NewMockAssumeRoleAPIClient(ctrl)

	stsClient.EXPECT().
		AssumeRole(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options),
		) (*sts.AssumeRoleOutput, error) {
			require.Equal(t, "arn:aws:iam::1:role/r", aws.ToString(in.RoleArn))
			require.Equal(t, "ext-1", aws.ToString(in.ExternalId))
			return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
				AccessKeyId:     aws.String("ASIA"),
				SecretAccessKey: aws.String("secret"),
				SessionToken:    aws.String("token"),
				Expiration:      aws.Time(time.Now().Add(time.Hour)),
			}}, nil
		})

	var captured aws.CredentialsProvider
	_, err := connectors3.NewSource(
		ctx,
		s3Cfg("iam_role"),
		stsClient,
		stubBuilder(sc, func(c aws.CredentialsProvider) { captured = c }),
	)
	require.NoError(t, err)
	creds, err := captured.Retrieve(ctx)
	require.NoError(t, err)
	require.Equal(t, "ASIA", creds.AccessKeyID)
}
