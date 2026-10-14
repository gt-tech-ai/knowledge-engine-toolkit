// Package outbox builds the OutboxSink a transactional-outbox relay delivers
// through: the zero-infrastructure stub (the default), an SQS queue, or an S3
// bucket, selected by Kind. Each real backend's SDK client runs through the client tier.
package outbox

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub accepts and discards every record (the default).
	KindStub = client.KindStub

	// KindSQS sends each record as an SQS message.
	KindSQS client.Kind = "sqs"

	// KindS3 writes each record as an S3 object.
	KindS3 client.Kind = "s3"
)

// Config selects and configures the sink.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// SQS configures KindSQS; its API comes from Deps.
	SQS outboxsqs.Config `yaml:"sqs" mapstructure:"sqs"`

	// S3 configures KindS3; its API comes from Deps.
	S3 outboxs3.Config `yaml:"s3" mapstructure:"s3"`

	// Name prefixes the SDK clients' names ("<name>.sqs", "<name>.s3") in decoration
	// overrides, metrics, spans and logs; empty uses "outbox".
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the sink's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// SQSAPI is the SQS SDK client KindSQS sends through.
	SQSAPI outboxsqs.API

	// S3API is the S3 SDK client KindS3 writes through.
	S3API outboxs3.API
}

// DefaultConfig returns the stub kind and the default S3 key template.
func DefaultConfig() Config {
	return Config{S3: outboxs3.Config{KeyTemplate: outboxs3.DefaultKeyTemplate}}
}

// tier builds the client tier for the SDK client kind of the sink cfg names.
func tier(kind string, cfg Config, deps Deps) (client.Base, error) {
	name := cfg.Name
	if name == "" {
		name = "outbox"
	}
	name += "." + kind
	d, err := deps.Deps(client.Tier(), name)
	if err != nil {
		return client.Base{}, err
	}
	return client.NewBase(name, d), nil
}

// kinds is the outbox-sink backend table. Each real backend runs its SDK client
// through the client tier, so the sink's own batching and routing stay undecorated.
func kinds() client.Kinds[Config, Deps, interfaces.OutboxSink] {
	return client.Kinds[Config, Deps, interfaces.OutboxSink]{
		Family:  "outbox",
		Default: KindStub,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.OutboxSink]{
			KindStub: func(context.Context, Config, Deps) (interfaces.OutboxSink, error) {
				return stub.New(), nil
			},
			KindSQS: func(
				_ context.Context, cfg Config, deps Deps,
			) (interfaces.OutboxSink, error) {
				api := deps.SQSAPI
				if api != nil {
					b, err := tier("sqs", cfg, deps)
					if err != nil {
						return nil, err
					}
					api = decorateSQS(api, b)
				}
				return outboxsqs.New(cfg.SQS, api) // reports a missing API
			},
			KindS3: func(
				_ context.Context, cfg Config, deps Deps,
			) (interfaces.OutboxSink, error) {
				api := deps.S3API
				if api != nil {
					b, err := tier("s3", cfg, deps)
					if err != nil {
						return nil, err
					}
					api = decorateS3(api, b)
				}
				return outboxs3.New(cfg.S3, api) // reports a missing API
			},
		},
	}
}

// NewFromConfig builds the sink cfg.Kind selects, its SDK client run through the
// client tier. An unknown kind, or a backend missing its API or required settings, is
// CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.OutboxSink, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}
