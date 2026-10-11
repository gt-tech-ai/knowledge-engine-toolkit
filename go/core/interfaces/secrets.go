package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Source resolves credentials from a configured backend — an environment variable, a
// git-ignored local file, or a cloud secret manager — selected by Kind at construction.
// It is the single seam through which any consumer reads or writes a credential, so
// switching a backend is a configuration change, not a caller edit.
type Source interface {
	// Get resolves the credential referenced by ref, returned as an opaque Secret. A
	// missing credential is a coded not-found error, not an empty Secret.
	Get(ctx context.Context, ref types.Ref) (types.Secret, error)

	// Put stores value for ref. Read-only backends (environment variables, and cloud
	// secret managers written out-of-band by IaC) return a coded unsupported-operation
	// error rather than silently succeeding.
	Put(ctx context.Context, ref types.Ref, value types.Secret) error
}
