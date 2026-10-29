package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Auth0Management reads an Auth0 tenant: its organizations through the Management API
// and its public token-signing key.
type Auth0Management interface {
	// OrganizationIDs returns the id of every organization in the tenant.
	OrganizationIDs(ctx context.Context) ([]string, error)

	// OrganizationID returns the id of the organization named name; an unknown name is
	// CodeNotFound.
	OrganizationID(ctx context.Context, name string) (string, error)

	// SigningKey returns the tenant's first JWKS signing key. It needs no Management
	// credentials: the key set is public.
	SigningKey(ctx context.Context) (types.SigningKey, error)
}
