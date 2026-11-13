// Package connector builds ConnectorSources for the connector family: S3 (a bucket
// listing), S3 Inventory (the bucket's inventory report) and GitHub (a branch's tree,
// as a GitHub App installation) now, a future kind added as a kinds-table entry +
// sibling package (ARCHITECTURE.md#the-one-idea). Unlike the other client families, a
// source is built per request, from the tenant's SourceConfig, so NewSourceBuilder
// returns the SourceBuilder that runs the kinds table. It imports only the storage
// INTERFACE — via the injected ClientBuilder — never go/clients/storage, so there is no
// lateral same-layer import.
package connector

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/github"
	connectors3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3inventory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// KindS3 is the S3-compatible object-store connector.
const KindS3 = client.Kind(interfaces.ConnectorKindS3)

// KindS3Inventory reads an S3 bucket's objects from its S3 Inventory report.
const KindS3Inventory = client.Kind(interfaces.ConnectorKindS3Inventory)

// KindGitHub lists one branch of a GitHub repository as a GitHub App installation.
const KindGitHub = client.Kind(interfaces.ConnectorKindGitHub)

// Config names the sources the builder decorates.
type Config struct {
	// Name prefixes each source's client name ("<name>-<kind>") in decoration
	// overrides, metrics, spans and logs; empty uses "connector".
	Name string `yaml:"name" mapstructure:"name"`

	// GitHubBaseURL is the GitHub REST API root KindGitHub calls; empty means
	// github.DefaultBaseURL (set it for GitHub Enterprise Server).
	GitHubBaseURL string `yaml:"github_base_url" mapstructure:"github_base_url"`
}

// Deps carries the builder's runtime collaborators.
type Deps struct {
	// Decorations decorates each source from the decoration config
	// (decoration.clients).
	decorate.Decorations

	// BuildS3Client constructs a connector-scoped StorageClient; injected by the app
	// root so this package never imports go/clients/storage.
	BuildS3Client connectors3.ClientBuilder
	// STS is the AssumeRole client used only by the iam_role strategy, which refuses
	// to build without one.
	STS stscreds.AssumeRoleAPIClient

	// GitHub carries KindGitHub's collaborators; nil uses an http.Client and time.Now.
	GitHub *GitHubDeps
}

// GitHubDeps carries KindGitHub's runtime collaborators.
type GitHubDeps struct {
	// Doer sends the REST requests; nil uses an http.Client (each request is still
	// bounded by the backend's 30-second timeout).
	Doer interfaces.HTTPDoer

	// Now reads the clock for the App JWT and the token cache; nil uses time.Now.
	Now func() time.Time
}

// kinds maps each connector Kind to its ConnectorSource backend under the builder's
// cfg. It has no default: a SourceConfig with no kind is refused.
func kinds(
	cfg *Config,
) client.Kinds[interfaces.SourceConfig, Deps, interfaces.ConnectorSource] {
	return client.Kinds[interfaces.SourceConfig, Deps, interfaces.ConnectorSource]{
		Family:   "connector",
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[
			interfaces.SourceConfig, Deps, interfaces.ConnectorSource,
		]{
			KindS3: func(
				ctx context.Context,
				cfg interfaces.SourceConfig,
				deps Deps,
			) (interfaces.ConnectorSource, error) {
				return connectors3.NewSource(ctx, &cfg, deps.STS, deps.BuildS3Client)
			},
			KindS3Inventory: func(
				ctx context.Context,
				cfg interfaces.SourceConfig,
				deps Deps,
			) (interfaces.ConnectorSource, error) {
				return s3inventory.NewSource(ctx, &cfg, deps.STS, deps.BuildS3Client)
			},
			KindGitHub: func(
				_ context.Context,
				src interfaces.SourceConfig,
				deps Deps,
			) (interfaces.ConnectorSource, error) {
				return newGitHub(cfg.GitHubBaseURL, &src, deps.GitHub)
			},
		},
		Options:    nil,
		Default:    "",
		AtMostOnce: nil,
	}
}

// builder is the connector SourceBuilder: it turns a SourceConfig into a decorated
// ConnectorSource through the kinds table.
type builder struct {
	// deps are the backends' collaborators.
	deps Deps
	// cfg tunes the client tier.
	cfg Config
}

// Ensure builder satisfies the SourceBuilder contract.
var _ interfaces.SourceBuilder = (*builder)(nil)

// NewSourceBuilder builds the SourceBuilder over cfg and deps.
func NewSourceBuilder(cfg Config, deps Deps) interfaces.SourceBuilder {
	return &builder{cfg: cfg, deps: deps}
}

// Build constructs the decorated ConnectorSource for src.Kind, named "<name>-<kind>";
// an unknown or empty kind is CodeInvalidInput.
// src must not be nil.
func (b *builder) Build(
	ctx context.Context,
	src *interfaces.SourceConfig,
) (interfaces.ConnectorSource, error) {
	prefix := b.cfg.Name
	if prefix == "" {
		prefix = "connector"
	}

	return kinds(&b.cfg).Build(
		ctx,
		client.Kind(src.Kind),
		*src,
		b.deps,
		b.deps.Decorations,
		prefix+"-"+string(src.Kind),
	)
}

// newGitHub builds the KindGitHub source for src at baseURL: the App installation's
// token source over src's App fields, then the tree lister over its repository,
// branch and globs.
func newGitHub(
	baseURL string,
	src *interfaces.SourceConfig,
	deps *GitHubDeps,
) (interfaces.ConnectorSource, error) {
	var (
		doer interfaces.HTTPDoer = &http.Client{
			Transport: nil, CheckRedirect: nil, Jar: nil, Timeout: 0,
		}
		now = time.Now
	)

	if deps != nil && deps.Doer != nil {
		doer = deps.Doer
	}

	if deps != nil && deps.Now != nil {
		now = deps.Now
	}

	auth, err := github.NewAppAuth(github.AppAuthConfig{
		BaseURL:        baseURL,
		PrivateKeyPEM:  src.AppPrivateKey,
		AppID:          src.GitHubAppID,
		InstallationID: src.InstallationID,
	}, doer, now)
	if err != nil {
		return nil, err
	}

	return github.NewSource(&github.Config{
		BaseURL:      baseURL,
		Owner:        src.Owner,
		Repo:         src.Repo,
		Branch:       src.Branch,
		IncludeGlobs: src.IncludeGlobs,
		ExcludeGlobs: src.ExcludeGlobs,
	}, auth, doer)
}
