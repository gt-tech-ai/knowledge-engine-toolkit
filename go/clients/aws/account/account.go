// Package account resolves a named environment to an AWS session verified against the
// account it must run in: the config is loaded from the ambient credential chain or the
// env's shared profile, the credentials are retrieved once and pinned, and STS confirms
// whose they are, so a command can never act with credentials for another account. A
// verified session is cached until it nears expiry. The shared-config writer for the
// profiles a sign-in flow owns is in profile.go.
//
// The resolver composes the decorated STS client (clients/aws/sts), so it is built with
// Kinds.New and not decorated again.
package account

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssts "github.com/aws/aws-sdk-go-v2/service/sts"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/singleflight"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/session"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub resolves every env to sts.StubAccount with no I/O (the default).
	KindStub client.Kind = "stub"

	// KindSDK loads AWS config through the SDK and verifies it with STS.
	KindSDK client.Kind = "sdk"
)

// Target is where, and as whom, one environment resolves.
type Target struct {
	// Profile is the shared-config profile loaded when no ambient chain is honored.
	Profile string

	// Region is the env's AWS region.
	Region string

	// ExpectedAccount is the account id the credentials must belong to.
	ExpectedAccount string

	// LoginHint is the remedy every unauthenticated error names (e.g. a sign-in
	// command).
	LoginHint string
}

// Session is an account-verified AWS session.
type Session struct {
	// AWS is the SDK config, its credentials pinned to the verified snapshot.
	AWS aws.Config

	// Creds is the verified credential snapshot.
	Creds aws.Credentials

	// Account is the verified account id.
	Account string

	// Region is the session's region.
	Region string

	// Profile is the shared profile loaded; empty when the ambient chain was used.
	Profile string
}

// ExpiresAt reports when the session's credentials expire; zero means never.
func (s *Session) ExpiresAt() time.Time {
	if !s.Creds.CanExpire {
		return time.Time{}
	}
	return s.Creds.Expires
}

// Compile-time assertion that a Session reports its expiry to the session cache rule.
var _ session.Expiring = (*Session)(nil)

// Resolver resolves an environment to its account-verified session.
type Resolver interface {
	// Resolve returns env's verified session. It honors an ambient credential chain
	// unless forceProfile, which loads the target's profile regardless (a sign-in flow,
	// so a stale export cannot shadow the fresh session).
	Resolve(ctx context.Context, env string, forceProfile bool) (Session, error)
}

// Config selects the resolver.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the STS client the resolver builds; empty uses "sts".
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the resolver's collaborators.
type Deps struct {
	// Decorations decorates the STS client each verification builds.
	decorate.Decorations

	// Target maps an env name to its Target (KindSDK; required).
	Target func(ctx context.Context, env string) (Target, error)

	// LoadAWS loads the SDK config for (profile, region); an empty profile lets the
	// ambient chain resolve. Nil uses the SDK's LoadDefaultConfig.
	LoadAWS func(ctx context.Context, profile, region string) (aws.Config, error)

	// STS replaces the STS client built from each loaded config.
	STS sts.API

	// Getenv reads the process environment for the ambient-chain variables; nil
	// means no ambient chain.
	Getenv func(string) string
}

// kinds is the resolver backend table.
func kinds() client.Kinds[Config, Deps, Resolver] {
	return client.Kinds[Config, Deps, Resolver]{
		Family:  "account",
		Default: KindStub,
		Backends: map[client.Kind]client.Backend[Config, Deps, Resolver]{
			KindStub: func(context.Context, Config, Deps) (Resolver, error) { return Stub{}, nil },
			KindSDK:  newSDK,
		},
	}
}

// NewFromConfig builds the resolver cfg.Kind selects; an unknown kind, or KindSDK with
// no Target, is CodeInvalidInput. It does no I/O.
func NewFromConfig(ctx context.Context, cfg Config, deps Deps) (Resolver, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}

// newSDK builds the SDK resolver, defaulting the loader and the environment reader.
func newSDK(_ context.Context, cfg Config, deps Deps) (Resolver, error) {
	if deps.Target == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, "account sdk: no target mapping")
	}
	if deps.LoadAWS == nil {
		deps.LoadAWS = loadDefault
	}
	if deps.Getenv == nil {
		deps.Getenv = func(string) string { return "" }
	}
	return &sdkResolver{cfg: cfg, deps: deps, cache: map[string]Session{}}, nil
}

// sdkResolver verifies sessions through STS and caches them until they near expiry.
type sdkResolver struct {
	// deps holds the collaborators.
	deps Deps

	// cache holds verified sessions by env and profile choice.
	cache map[string]Session

	// group collapses concurrent resolutions of one key into one.
	group singleflight.Group

	// cfg names the STS client.
	cfg Config

	// mu guards cache.
	mu sync.Mutex
}

// Resolve serves a cached, unexpired session or resolves a fresh one.
func (r *sdkResolver) Resolve(
	ctx context.Context,
	env string,
	forceProfile bool,
) (Session, error) {
	key := fmt.Sprintf("%s\x00%t", env, forceProfile)
	if s, ok := r.cached(key); ok {
		return s, nil
	}
	v, err, _ := r.group.Do(key, func() (any, error) {
		s, err := r.resolve(ctx, env, forceProfile)
		if err != nil {
			return Session{}, err
		}
		r.mu.Lock()
		r.cache[key] = s
		r.mu.Unlock()
		return s, nil
	})
	s, _ := v.(Session)
	return s, err
}

// cached returns key's session unless it expires within session.ExpirySkew.
func (r *sdkResolver) cached(key string) (Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.cache[key]
	if !ok {
		return Session{}, false
	}
	if at := s.ExpiresAt(); !at.IsZero() &&
		at.Before(time.Now().Add(session.ExpirySkew)) {
		delete(r.cache, key)
		return Session{}, false
	}
	return s, true
}

// resolve loads the config, pins the retrieved credentials and asserts the account.
func (r *sdkResolver) resolve(
	ctx context.Context,
	env string,
	forceProfile bool,
) (Session, error) {
	t, err := r.deps.Target(ctx, env)
	if err != nil {
		return Session{}, err
	}
	profile := ""
	if forceProfile || !r.ambient() {
		profile = t.Profile
	}
	awscfg, err := r.deps.LoadAWS(ctx, profile, t.Region)
	if err != nil {
		return Session{}, apperr.Wrap(err, apperr.CodeUnauthorized,
			"load AWS config for "+env+": "+t.LoginHint)
	}
	creds, err := awscfg.Credentials.Retrieve(ctx)
	if err != nil {
		return Session{}, apperr.Wrap(err, apperr.CodeUnauthorized,
			"retrieve AWS credentials for "+env+": "+t.LoginHint)
	}
	// Pin the snapshot so the STS call, and every later use, acts on exactly the
	// credentials verified here.
	awscfg.Credentials = credentials.StaticCredentialsProvider{Value: creds}

	api, err := r.stsFor(ctx, &awscfg)
	if err != nil {
		return Session{}, err
	}
	out, err := api.GetCallerIdentity(ctx, &awssts.GetCallerIdentityInput{})
	if err != nil {
		return Session{}, mapSTSError(err, env, t.LoginHint)
	}
	if got := aws.ToString(out.Account); got != t.ExpectedAccount {
		return Session{}, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"wrong account: credentials are for %s, expected %s for %s",
			got, t.ExpectedAccount, env))
	}
	return Session{
		AWS: awscfg, Creds: creds, Account: t.ExpectedAccount,
		Region: t.Region, Profile: profile,
	}, nil
}

// stsFor returns Deps.STS, else the decorated SDK STS client over awscfg.
func (r *sdkResolver) stsFor(ctx context.Context, awscfg *aws.Config) (sts.API, error) {
	if r.deps.STS != nil {
		return r.deps.STS, nil
	}
	return sts.NewFromConfig(ctx,
		sts.Config{Kind: sts.KindSDK, Name: r.cfg.Name},
		sts.Deps{Decorations: r.deps.Decorations, AWS: awscfg})
}

// ambient reports whether an ambient credential chain is configured: a static key, a
// web identity (EKS/IRSA) or container credentials (ECS/EKS).
func (r *sdkResolver) ambient() bool {
	for _, k := range []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	} {
		if r.deps.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// mapSTSError classifies a GetCallerIdentity failure: an auth-class API error code
// (missing, expired or unusable session) is CodeUnauthorized naming the login hint;
// anything else is an unavailable upstream.
func mapSTSError(err error, env, hint string) error {
	var apiErr smithy.APIError
	if apperr.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ExpiredToken", "InvalidClientTokenId", "UnrecognizedClientException",
			"AccessDenied":
			return apperr.Wrap(err, apperr.CodeUnauthorized,
				"AWS session for "+env+" is not valid: "+hint)
		}
	}
	return apperr.Wrap(err, apperr.CodeUnavailable, "sts:GetCallerIdentity")
}

// loadDefault is the SDK loader: LoadDefaultConfig in region, forcing the shared
// profile only when profile is non-empty.
func loadDefault(ctx context.Context, profile, region string) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	return cfg, apperr.Wrap(err, apperr.CodeInternal, "config.LoadDefaultConfig")
}

// Stub is the resolver that does no I/O.
type Stub struct{}

// Compile-time assertion that Stub satisfies Resolver.
var _ Resolver = Stub{}

// Resolve returns a never-expiring session in sts.StubAccount.
func (Stub) Resolve(context.Context, string, bool) (Session, error) {
	return Session{Account: sts.StubAccount}, nil
}
