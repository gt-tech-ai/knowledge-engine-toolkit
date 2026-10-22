package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/account"
	kests "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// accountTarget is the target every resolver test resolves "stg" to.
func accountTarget(context.Context, string) (account.Target, error) {
	return account.Target{
		Profile:         "acme-stg",
		Region:          "us-east-1",
		ExpectedAccount: "111122223333",
		LoginHint:       "run `acme login stg`",
	}, nil
}

// credsUntil returns an aws.Config whose credentials expire at exp (zero: never).
func credsUntil(exp time.Time) aws.Config {
	return aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(
			func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID: "AKIA", SecretAccessKey: "secret", SessionToken: "tok",
					CanExpire: !exp.IsZero(), Expires: exp,
				}, nil
			}),
	}
}

// newAccountResolver builds an sdk-kind resolver over api and load.
func newAccountResolver(
	t *testing.T,
	api kests.API,
	load func(ctx context.Context, profile, region string) (aws.Config, error),
	getenv func(string) string,
) account.Resolver {
	t.Helper()
	r, err := account.NewFromConfig(t.Context(),
		account.Config{Kind: account.KindSDK},
		account.Deps{Target: accountTarget, LoadAWS: load, STS: api, Getenv: getenv})
	require.NoError(t, err)
	return r
}

// identity returns a GetCallerIdentity output for acct.
func identity(acct string) *sts.GetCallerIdentityOutput {
	return &sts.GetCallerIdentityOutput{Account: aws.String(acct)}
}

// TestAccountResolver_VerifiesTheAccount tests that a session is served only for the
// env's expected account.
//
// Why this test is important:
//   - A command that runs under credentials for another account deploys to, or deletes
//     from, the wrong cloud; the resolver is the one place that refuses it.
//
// What it tests:
//   - A matching caller identity yields a Session naming the account, region and the
//     profile it loaded, with the retrieved credentials pinned.
//   - A mismatch is INVALID_INPUT naming both accounts and the env.
func TestAccountResolver_VerifiesTheAccount(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	load := func(context.Context, string, string) (aws.Config, error) {
		return credsUntil(time.Time{}), nil
	}

	api := mocks.NewMockAWSSTSAPI(gomock.NewController(t))
	api.EXPECT().GetCallerIdentity(gomock.Any(), gomock.Any()).
		Return(identity("111122223333"), nil)
	s, err := newAccountResolver(t, api, load, nil).Resolve(ctx, "stg", false)
	require.NoError(t, err)
	assert.Equal(t, "111122223333", s.Account)
	assert.Equal(t, "us-east-1", s.Region)
	assert.Equal(t, "acme-stg", s.Profile, "no ambient chain: the profile is forced")
	assert.Equal(t, "AKIA", s.Creds.AccessKeyID)
	pinned, err := s.AWS.Credentials.Retrieve(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tok", pinned.SessionToken, "the verified snapshot is pinned")

	wrong := mocks.NewMockAWSSTSAPI(gomock.NewController(t))
	wrong.EXPECT().GetCallerIdentity(gomock.Any(), gomock.Any()).
		Return(identity("999988887777"), nil)
	_, err = newAccountResolver(t, wrong, load, nil).Resolve(ctx, "stg", false)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)
	assert.Contains(t, err.Error(),
		"wrong account: credentials are for 999988887777, expected 111122223333 for stg")
}

// TestAccountResolver_AuthFailuresNameTheLogin tests that every unauthenticated path is
// UNAUTHORIZED with the target's login hint.
//
// Why this test is important:
//   - An operator with an expired SSO session must be told how to sign in, not shown a
//     raw SDK error; any other STS failure is an outage, not a login problem.
//
// What it tests:
//   - A config load failure and an auth-class STS error code are UNAUTHORIZED naming
//     the hint; a non-auth STS error is UNAVAILABLE.
func TestAccountResolver_AuthFailuresNameTheLogin(t *testing.T) {
	t.Parallel()
	ok := func(context.Context, string, string) (aws.Config, error) {
		return credsUntil(time.Time{}), nil
	}

	failLoad := func(context.Context, string, string) (aws.Config, error) {
		return aws.Config{}, apperr.Sentinel("no sso token")
	}
	_, err := newAccountResolver(t, nil, failLoad, nil).Resolve(t.Context(), "stg", false)
	assert.True(t, apperr.Is(err, apperr.CodeUnauthorized), "got %v", err)
	assert.Contains(t, err.Error(), "run `acme login stg`")

	cases := []struct {
		err  error
		name string
		code apperr.ErrorCode
	}{
		{
			&smithy.GenericAPIError{Code: "ExpiredToken"},
			"expired token",
			apperr.CodeUnauthorized,
		},
		{
			&smithy.GenericAPIError{Code: "AccessDenied"},
			"access denied",
			apperr.CodeUnauthorized,
		},
		{
			&smithy.GenericAPIError{Code: "Throttling"},
			"throttled",
			apperr.CodeUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := mocks.NewMockAWSSTSAPI(gomock.NewController(t))
			api.EXPECT().GetCallerIdentity(gomock.Any(), gomock.Any()).Return(nil, tc.err)
			_, err := newAccountResolver(
				t,
				api,
				ok,
				nil,
			).Resolve(t.Context(), "stg", false)
			assert.True(t, apperr.Is(err, tc.code), "got %v", err)
			if tc.code == apperr.CodeUnauthorized {
				assert.Contains(t, err.Error(), "run `acme login stg`")
			}
		})
	}
}

// TestAccountResolver_CachesUntilExpiry tests that an env is verified once per session.
//
// Why this test is important:
//   - A command resolves its env on every AWS call; an STS round trip each time is
//     slow, and an expired session served from the cache fails every later call.
//
// What it tests:
//   - Two resolves of a far-future session make one STS call; a session inside
//     ExpirySkew is verified again on each resolve.
func TestAccountResolver_CachesUntilExpiry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		exp   time.Time
		name  string
		calls int
	}{
		{time.Now().Add(time.Hour), "far future is cached", 1},
		{time.Now().Add(time.Minute), "inside the skew re-resolves", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := mocks.NewMockAWSSTSAPI(gomock.NewController(t))
			api.EXPECT().GetCallerIdentity(gomock.Any(), gomock.Any()).
				Return(identity("111122223333"), nil).Times(tc.calls)
			load := func(context.Context, string, string) (aws.Config, error) {
				return credsUntil(tc.exp), nil
			}
			r := newAccountResolver(t, api, load, nil)
			for range 2 {
				s, err := r.Resolve(t.Context(), "stg", false)
				require.NoError(t, err)
				assert.WithinDuration(t, tc.exp, s.ExpiresAt(), time.Second)
			}
		})
	}
}

// TestAccountResolver_AmbientChain tests when the resolver honors ambient credentials.
//
// Why this test is important:
//   - In CI and in a pod the credentials are ambient (a static key, a web identity);
//     forcing a shared profile there fails. A sign-in flow must still force the profile
//     so a stale export cannot shadow the fresh session.
//
// What it tests:
//   - An ambient variable loads with no profile; forceProfile loads the target's
//     profile despite it.
func TestAccountResolver_AmbientChain(t *testing.T) {
	t.Parallel()
	getenv := func(k string) string {
		if k == "AWS_WEB_IDENTITY_TOKEN_FILE" {
			return "/var/run/token"
		}
		return ""
	}
	var profiles []string
	load := func(_ context.Context, profile, _ string) (aws.Config, error) {
		profiles = append(profiles, profile)
		return credsUntil(time.Time{}), nil
	}
	api := mocks.NewMockAWSSTSAPI(gomock.NewController(t))
	api.EXPECT().GetCallerIdentity(gomock.Any(), gomock.Any()).
		Return(identity("111122223333"), nil).Times(2)
	r := newAccountResolver(t, api, load, getenv)

	_, err := r.Resolve(t.Context(), "stg", false)
	require.NoError(t, err)
	_, err = r.Resolve(t.Context(), "stg", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"", "acme-stg"}, profiles)
}

// TestAccountResolver_StubDoesNoIO tests the default kind.
//
// Why this test is important:
//   - The composition root boots on the default kind with no AWS account; a stub that
//     loaded config or called STS would fail there.
//
// What it tests:
//   - The empty kind resolves any env to StubAccount without calling the injected
//     loader; an unknown kind is INVALID_INPUT.
func TestAccountResolver_StubDoesNoIO(t *testing.T) {
	t.Parallel()
	load := func(context.Context, string, string) (aws.Config, error) {
		t.Fatal("the stub loaded AWS config")
		return aws.Config{}, nil
	}
	r, err := account.NewFromConfig(t.Context(), account.Config{},
		account.Deps{Target: accountTarget, LoadAWS: load})
	require.NoError(t, err)
	s, err := r.Resolve(t.Context(), "anything", false)
	require.NoError(t, err)
	assert.Equal(t, kests.StubAccount, s.Account)

	_, err = account.NewFromConfig(
		t.Context(),
		account.Config{Kind: "nope"},
		account.Deps{},
	)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)
}

// stgProfile is the profile the writer tests upsert.
func stgProfile() account.Profile {
	return account.Profile{
		Name:        "acme-stg",
		SSOSession:  "acme",
		SSOStartURL: "https://acme.awsapps.com/start",
		SSORegion:   "us-east-1",
		AccountID:   "111122223333",
		RoleName:    "Admin",
		Region:      "us-west-2",
	}
}

// TestUpsertProfile tests the owner-managed block of the shared AWS config.
//
// Why this test is important:
//   - Sign-in writes the operator's ~/.aws/config; it must never touch their own
//     stanzas, never append duplicates on a re-run, and never be shadowed by a
//     hand-written stanza of the same name that the AWS CLI would merge over it.
//
// What it tests:
//   - The block is appended after the operator's text, a re-run is byte-identical,
//     the file is 0600; a same-named stanza outside the block is INVALID_INPUT and
//     leaves the file untouched.
func TestUpsertProfile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aws", "config")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(
		t,
		os.WriteFile(path, []byte("[profile other]\nregion = eu-west-1\n"), 0o600),
	)

	require.NoError(t, account.UpsertProfile(path, "acme", stgProfile()))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(first)
	assert.True(t, strings.HasPrefix(out, "[profile other]\nregion = eu-west-1\n"))
	assert.Contains(t, out, "# >>> acme managed\n[sso-session acme]\n")
	assert.Contains(t, out, "[profile acme-stg]\nsso_session = acme\n"+
		"sso_account_id = 111122223333\nsso_role_name = Admin\nregion = us-west-2\n")

	require.NoError(t, account.UpsertProfile(path, "acme", stgProfile()))
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, out, string(again), "a re-run is byte-identical")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dup := filepath.Join(t.TempDir(), "config")
	original := "[sso-session acme]\nsso_region = eu-west-1\n"
	require.NoError(t, os.WriteFile(dup, []byte(original), 0o600))
	err = account.UpsertProfile(dup, "acme", stgProfile())
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)
	assert.Contains(t, err.Error(), "[sso-session acme]")
	got, err := os.ReadFile(dup)
	require.NoError(t, err)
	assert.Equal(t, original, string(got), "the file is untouched")
}

// TestConfigPath tests where the shared AWS config lives.
//
// Why this test is important:
//   - The writer and every reader must agree on one file; AWS_CONFIG_FILE redirects it.
//
// What it tests:
//   - AWS_CONFIG_FILE wins; otherwise the path ends in .aws/config.
func TestConfigPath(t *testing.T) {
	t.Parallel()
	got, err := account.ConfigPath(func(k string) string {
		if k == "AWS_CONFIG_FILE" {
			return "/tmp/custom/config"
		}
		return ""
	})
	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom/config", got)

	got, err = account.ConfigPath(func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(".aws", "config"),
		filepath.Join(filepath.Base(filepath.Dir(got)), filepath.Base(got)))
}
