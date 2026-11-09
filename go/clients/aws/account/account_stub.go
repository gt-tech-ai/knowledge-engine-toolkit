package account

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts"
)

// Stub is the resolver that does no I/O.
type Stub struct{}

// Resolve returns a never-expiring session in sts.StubAccount.
func (Stub) Resolve(context.Context, string, bool) (Session, error) {
	return Session{Account: sts.StubAccount}, nil
}
