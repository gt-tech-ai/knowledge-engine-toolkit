package ops

import (
	"fmt"
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// admits reports whether policy p admits the operation d describes: its class ranks at
// or below MaxSafety, its lifecycle is listed, it is in Allow (when Allow is set), and
// it is not in Deny. Both ranks are known — the policy and descriptor validated first.
func admits(
	exposurePolicy *types.ExposurePolicy,
	operationDescriptor *types.OperationDescriptor,
) bool {
	opRank, _ := operationDescriptor.SafetyClass.Rank()
	maxRank, _ := exposurePolicy.MaxSafety.Rank()

	return opRank <= maxRank &&
		slices.Contains(exposurePolicy.Lifecycles, operationDescriptor.Lifecycle) &&
		(len(exposurePolicy.Allow) == 0 || slices.Contains(exposurePolicy.Allow,
			operationDescriptor.Name)) &&
		!slices.Contains(exposurePolicy.Deny, operationDescriptor.Name)
}

// checkPolicyNames fails with CodeInvalidInput when the policy's allow or deny list
// names an operation known does not contain.
func checkPolicyNames(p *types.ExposurePolicy, known map[string]bool) error {
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
