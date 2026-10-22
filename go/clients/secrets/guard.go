package secrets

import (
	"fmt"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// GuardDomain refuses a resolved credential whose domain (a tenant or issuer host) is
// not the expected one, so a credential for one tenant is never used against another.
// An empty resolved domain is a mismatch: a credential with no domain cannot be proven
// safe. subject names the credential in the error (e.g. `Auth0 credentials for env
// "stg"`). Surrounding space is ignored; a mismatch is CodeInvalidInput.
func GuardDomain(subject, expected, resolved string) error {
	got := strings.TrimSpace(resolved)
	if got == "" || got != strings.TrimSpace(expected) {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"refusing %s: resolved domain %q does not match the expected %q",
			subject, resolved, expected))
	}
	return nil
}
