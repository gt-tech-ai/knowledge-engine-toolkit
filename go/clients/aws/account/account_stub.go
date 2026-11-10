package account

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// Stub is the resolver that does no I/O.
type Stub struct{}

// Resolve returns a never-expiring session in sts.StubAccount.
func (Stub) Resolve(context.Context, string, bool) (Session, error) {
	return Session{
		Account:   sts.StubAccount,
		AWS:       stdx.Zero[aws.Config](),
		Creds:     stdx.Zero[aws.Credentials](),
		Region:    "",
		Profile:   "",
		Principal: "",
	}, nil
}
