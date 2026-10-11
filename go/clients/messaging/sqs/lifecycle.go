package sqs

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time checks: the publisher and subscriber are lifecycle-managed so a
// lifecycle.Manager registers them uniformly alongside the connection-oriented
// clients (redis, dbpool, grpc). Both embed lifecycle.NoOp for their Start/Stop:
// the SQS SDK client is stateless (each Publish/Receive is an independent request),
// so there is no persistent connection to open or close.
var (
	// Publisher is lifecycle-managed (no-op Start/Stop; stateless SDK client).
	_ interfaces.Lifecycle = (*Publisher)(nil)
	// Subscriber is lifecycle-managed (no-op Start/Stop; stateless SDK client).
	_ interfaces.Lifecycle = (*Subscriber)(nil)
)
