// Package connector builds ConnectorSources for the connector family: S3 (a bucket
// listing) and S3 Inventory (the bucket's inventory report) now, a future
// GDrive/Salesforce kind added as a kinds-table entry + sibling package
// (ARCHITECTURE.md#the-one-idea). Unlike the other client families, a source is built
// per request, from the tenant's SourceConfig, so NewSourceBuilder returns the
// SourceBuilder that runs the kinds table. It imports only the storage INTERFACE — via
// the injected ClientBuilder — never go/clients/storage, so there is no lateral
// same-layer import.
package connector

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	connectors3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3inventory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// KindS3 is the S3-compatible object-store connector.
const KindS3 = client.Kind(interfaces.ConnectorKindS3)

// KindS3Inventory reads an S3 bucket's objects from its S3 Inventory report.
const KindS3Inventory = client.Kind(interfaces.ConnectorKindS3Inventory)

// Config names the sources the builder decorates.
type Config struct {
	// Name prefixes each source's client name ("<name>-<kind>") in decoration
	// overrides, metrics, spans and logs; empty uses "connector".
	Name string `yaml:"name" mapstructure:"name"`
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
}

// kinds maps each connector Kind to its ConnectorSource backend. It has no default: a
// SourceConfig with no kind is refused.
func kinds() client.Kinds[interfaces.SourceConfig, Deps, interfaces.ConnectorSource] {
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
		},
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

	return kinds().Build(
		ctx,
		client.Kind(src.Kind),
		*src,
		b.deps,
		b.deps.Decorations,
		prefix+"-"+string(src.Kind),
	)
}
