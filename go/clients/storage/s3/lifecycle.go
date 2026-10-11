package s3

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time check: the S3 client is lifecycle-managed so a lifecycle.Manager
// registers it uniformly alongside the connection-oriented clients. It embeds
// lifecycle.NoOp for Start/Stop: the S3 SDK client is stateless (each request is an
// independent HTTPS call), so there is no persistent connection to open or close.
var _ interfaces.Lifecycle = (*s3Client)(nil)
