// Package outbox builds the OutboxSink a transactional-outbox relay delivers
// through: the zero-infrastructure stub (the default), an SQS queue, or an S3
// bucket. Each real backend's SDK client is wrapped in the client resilience
// stack (see the decorators subpackage).
package outbox

import (
	"fmt"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/decorators"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Kind selects the sink backend.
type Kind int

const (
	// KindStub accepts and discards every record (the default).
	KindStub Kind = iota
	// KindSQS sends each record as an SQS message.
	KindSQS
	// KindS3 writes each record as an S3 object.
	KindS3
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindStub:
		return "stub"
	case KindSQS:
		return "sqs"
	case KindS3:
		return "s3"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config selects and configures the sink.
type Config struct {
	// SQS configures KindSQS.
	SQS outboxsqs.Config `yaml:"sqs" mapstructure:"sqs"`
	// S3 configures KindS3.
	S3 outboxs3.Config `yaml:"s3" mapstructure:"s3"`
	// Resilience tunes the client stack around the SDK calls (retry off by
	// default, so it never doubles up with the SDK's own retries).
	Resilience clientdecorators.Config `yaml:"resilience" mapstructure:"resilience"`
	// Kind selects the backend.
	Kind Kind `yaml:"kind" mapstructure:"kind"`
}

// DefaultConfig returns the stub kind, the default S3 key template and the
// default client stack.
func DefaultConfig() Config {
	return Config{
		Kind:       KindStub,
		S3:         outboxs3.Config{KeyTemplate: outboxs3.DefaultKeyTemplate},
		Resilience: clientdecorators.DefaultConfig(),
	}
}

// NewFromConfig builds the sink cfg selects, its SDK client wrapped in the
// client stack built from cfg.Resilience and deps. An unknown kind, or a backend
// missing its required settings, is CodeInvalidInput.
func NewFromConfig(
	cfg Config,
	deps clientdecorators.Deps,
) (interfaces.OutboxSink, error) {
	switch cfg.Kind {
	case KindStub:
		return stub.New(), nil
	case KindSQS:
		if cfg.SQS.API == nil {
			return outboxsqs.New(cfg.SQS) // reports the missing API
		}
		stack, err := clientdecorators.StackFromConfig("outbox.sqs", cfg.Resilience, deps)
		if err != nil {
			return nil, err
		}
		cfg.SQS.API = decorators.SQSAPI(cfg.SQS.API, stack)
		return outboxsqs.New(cfg.SQS)
	case KindS3:
		if cfg.S3.API == nil {
			return outboxs3.New(cfg.S3) // reports the missing API
		}
		stack, err := clientdecorators.StackFromConfig("outbox.s3", cfg.Resilience, deps)
		if err != nil {
			return nil, err
		}
		cfg.S3.API = decorators.S3API(cfg.S3.API, stack)
		return outboxs3.New(cfg.S3)
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown outbox sink kind: %v", cfg.Kind),
		)
	}
}
