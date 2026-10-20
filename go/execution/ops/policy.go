package ops

import (
	"fmt"
	"slices"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// admits reports whether policy p admits the operation d describes: its class ranks at
// or below MaxSafety, its lifecycle is listed, it is in Allow (when Allow is set), and
// it is not in Deny. Both ranks are known — the policy and descriptor validated first.
func admits(p types.ExposurePolicy, d types.OperationDescriptor) bool {
	opRank, _ := d.SafetyClass.Rank()
	maxRank, _ := p.MaxSafety.Rank()
	return opRank <= maxRank &&
		slices.Contains(p.Lifecycles, d.Lifecycle) &&
		(len(p.Allow) == 0 || slices.Contains(p.Allow, d.Name)) &&
		!slices.Contains(p.Deny, d.Name)
}

// checkPolicyNames fails with CodeInvalidInput when the policy's allow or deny list
// names an operation known does not contain.
func checkPolicyNames(p types.ExposurePolicy, known map[string]bool) error {
	for _, list := range [][]string{p.Allow, p.Deny} {
		for _, name := range list {
			if !known[name] {
				return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
					"exposure policy names unknown operation %q", name,
				))
			}
		}
	}
	return nil
}
