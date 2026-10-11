package interfaces

import "context"

// Pipeline is the generic data transformation interface for stateless sequential
// operations. In = input type, Out = output type.
//
// Pipelines model stateless sequential transformations where each stage consumes the
// output of the prior stage. Unlike workflows, pipelines do not coordinate state or
// orchestrate multiple services — they focus on pure data transformation.
//
// Examples: NLP processing (tokenization → POS tagging → NER), document parsing
// (text extraction → metadata extraction → chunking), ETL transformations.
type Pipeline[In any, Out any] interface {
	// Execute transforms the input into output.
	// Context is provided for cancellation and tracing.
	// Implementations should be stateless and safe for concurrent use.
	Execute(ctx context.Context, input In) (Out, error)
}
