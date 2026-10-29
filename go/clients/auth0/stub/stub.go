// Package stub is the auth0 family's zero-infrastructure backend: it reaches no tenant,
// so every call is CodeUnavailable rather than an empty answer.
package stub

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client reaches no tenant.
type Client struct{}

// Compile-time assertion that Client satisfies Auth0Management.
var _ interfaces.Auth0Management = Client{}

// unavailable is the error every call returns.
func unavailable() error {
	return apperr.New(apperr.CodeUnavailable, "auth0: the client is a stub")
}

// OrganizationIDs is CodeUnavailable.
func (Client) OrganizationIDs(context.Context) ([]string, error) {
	return nil, unavailable()
}

// OrganizationID is CodeUnavailable.
func (Client) OrganizationID(context.Context, string) (string, error) {
	return "", unavailable()
}

// SigningKey is CodeUnavailable.
func (Client) SigningKey(context.Context) (types.SigningKey, error) {
	return types.SigningKey{}, unavailable()
}
