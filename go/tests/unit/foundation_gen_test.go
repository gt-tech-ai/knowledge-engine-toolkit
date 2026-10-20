package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestFoundationGen_CheckAndApply tests the shared drift check and apply.
//
// Why this test is important:
//   - Every generator's CI drift check depends on it: drift that goes unreported ships
//     stale generated code, and a diff that names the wrong file sends the reader to
//     the wrong place.
//
// What it tests:
//   - A changed output and a missing output are drift, listed in path order, with a
//     unified diff (a missing file diffs from /dev/null); matching outputs are in
//     sync; Apply writes every artifact in path order.
func TestFoundationGen_CheckAndApply(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	g := mocks.NewMockGenerator(gomock.NewController(t))
	fresh := types.Artifacts{"gen/b.txt": []byte("new\n"), "gen/a.txt": []byte("x\n")}
	g.EXPECT().Render(gomock.Any(), gomock.Any()).Return(fresh, nil).AnyTimes()

	d, err := gen.Check(ctx, memoryCodebase(t, map[string]string{"gen/b.txt": "old\n"}), g)
	require.NoError(t, err)
	assert.False(t, d.InSync)
	assert.Equal(t, []string{"gen/a.txt", "gen/b.txt"}, d.Paths)
	assert.Contains(t, d.Diff, "--- /dev/null\n+++ gen/a.txt\n")
	assert.Contains(t, d.Diff, "--- gen/b.txt\n+++ gen/b.txt\n@@ -1 +1 @@\n-old\n+new\n")

	d, err = gen.Check(ctx, memoryCodebase(t, map[string]string{
		"gen/a.txt": "x\n", "gen/b.txt": "new\n",
	}), g)
	require.NoError(t, err)
	assert.True(t, d.InSync)
	assert.Empty(t, d.Diff)

	var order []string
	require.NoError(t, gen.Apply(ctx, func(p string, _ []byte) error {
		order = append(order, p)
		return nil
	}, fresh))
	assert.Equal(t, []string{"gen/a.txt", "gen/b.txt"}, order)
}
