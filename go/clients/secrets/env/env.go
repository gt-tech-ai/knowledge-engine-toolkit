// Package env implements the environment-variable credential Source: it resolves a Ref
// to a configured environment variable name and reads its value via os.Getenv. It is the
// backend subpackage of clients/secrets, selected by KindEnv. It is read-only — Put
// returns a coded error — because environment variables are set out-of-band (the shell,
// the container spec, the CI secret store), never written by the process.
package env

import (
	"context"
	"fmt"
	"os"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// source resolves credentials from environment variables named by a caller-supplied
// Ref→variable-name mapping (the mapping is policy; the source is the mechanism).
type source struct {
	// vars maps a credential Ref to the environment variable that holds its value.
	vars map[types.Ref]string
}

// New builds an environment-variable Source over the given Ref→variable-name mapping.
func New(vars map[types.Ref]string) interfaces.Source {
	return source{vars: vars}
}

// Get resolves ref to its environment variable and returns the value as an opaque
// Secret. An unmapped ref, or a mapped variable that is unset or empty, is a coded
// not-found error — never an empty Secret a caller might mistake for a real value.
func (s source) Get(_ context.Context, ref types.Ref) (types.Secret, error) {
	name, ok := s.vars[ref]
	if !ok {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf("no environment variable mapped for credential %s", ref),
		)
	}
	value := os.Getenv(name)
	if value == "" {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf("environment variable %s is unset for credential %s", name, ref),
		)
	}
	return types.NewSecret(value), nil
}

// Put is unsupported: environment variables are set out-of-band, never written by the
// process. It returns a coded error so a caller routing a write to this backend fails
// loudly instead of silently losing the value.
func (s source) Put(_ context.Context, ref types.Ref, _ types.Secret) error {
	return apperr.New(
		apperr.CodeInvalidInput,
		fmt.Sprintf("env credential source is read-only: cannot write %s", ref),
	)
}
