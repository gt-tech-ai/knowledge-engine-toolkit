package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// toolEntries decodes testdata/toolchain/tools.yaml, a small manifest with one tool
// of every shape, and checks it is valid.
func toolEntries(t *testing.T) []toolchain.ToolEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "toolchain", "tools.yaml"))
	require.NoError(t, err)
	var tools []toolchain.ToolEntry
	require.NoError(t, yaml.Unmarshal(raw, &tools))
	require.Empty(t, toolchain.ValidateToolEntries(tools))
	return tools
}

// denyPrompter is a Prompter that declines every confirmation as a non-interactive
// session does.
func denyPrompter(t *testing.T) interfaces.Prompter {
	t.Helper()
	p := mocks.NewMockPrompter(gomock.NewController(t))
	p.EXPECT().Confirm(gomock.Any(), gomock.Any()).
		Return(false, interfaces.NonInteractiveError()).AnyTimes()
	return p
}

// registerToolScanMock makes every probed tool present and every version probe
// return empty output, so a tool scan succeeds hermetically; a test layers its own
// Run expectations on top.
func registerToolScanMock(r *mocks.MockCommandRunner) {
	r.EXPECT().Exists(gomock.Any()).Return(true).AnyTimes()
	for i := range 9 {
		args := make([]any, 0, 3+i)
		for range 3 + i {
			args = append(args, gomock.Any())
		}
		r.EXPECT().RunBuffered(args[0], args[1], args[2], args[3:]...).
			Return(interfaces.CmdResult{}).AnyTimes()
	}
}
