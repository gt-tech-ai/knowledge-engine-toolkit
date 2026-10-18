package unit_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/opmockgen"
	gen "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/opmockgen"
)

// opmockgenFixture is the import path of the package opmockgen's fixture lives in.
const opmockgenFixture = "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/opmockgen"

// TestOpmockgen_GeneratesTheCommittedMock tests opmockgen.Generate.
//
// Why this test is important:
//   - A consumer commits (or regenerates) the contract mocks and a drift check compares
//     them; the generator must be deterministic and its fixture output current.
//   - A name that is not a struct, or a field that is neither an interface nor an
//     embedded struct, must fail loudly instead of writing a mock that hides a field.
//
// What it tests:
//   - Generating the fixture's Notes reproduces the committed mock_ops_gen.go byte for
//     byte: one engine mock per role and operation, flattened through the embedded
//     struct.
//   - A contract-local interface is mocked by the unqualified mockgen mock of its name,
//     and a contract from another package is written qualified.
//   - Two fields promoted to one name, an unknown name, a non-struct, a struct with a non-interface field, a missing
//     package name and no structs are CodeInvalidInput.
func TestOpmockgen_GeneratesTheCommittedMock(t *testing.T) {
	t.Parallel()
	got, err := gen.Generate(gen.Config{
		Source:      opmockgenFixture,
		Package:     "opmockgen",
		PackagePath: opmockgenFixture,
		Structs:     []string{"Notes"},
	})
	require.NoError(t, err)
	want, err := os.ReadFile("../fixtures/opmockgen/mock_ops_gen.go")
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	local, err := gen.Generate(gen.Config{
		Source: opmockgenFixture, Package: "mocks", Structs: []string{"Tagged"},
	})
	require.NoError(t, err)
	assert.Contains(t, string(local), "Tagger: NewMockTagger(ctrl)")
	assert.Contains(t, string(local), "func (m *MockTagged) Contract() opmockgen.Tagged {")

	for name, bad := range map[string]gen.Config{
		"promoted clash":    {Source: opmockgenFixture, Package: "p", Structs: []string{"Clash"}},
		"unknown name":      {Source: opmockgenFixture, Package: "p", Structs: []string{"Nope"}},
		"not a struct":      {Source: "context", Package: "p", Structs: []string{"CancelFunc"}},
		"non-iface field":   {Source: "sync", Package: "p", Structs: []string{"WaitGroup"}},
		"no package":        {Source: opmockgenFixture, Structs: []string{"Notes"}},
		"no structs listed": {Source: opmockgenFixture, Package: "p"},
	} {
		_, err := gen.Generate(bad)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
}

// TestOpmockgen_ContractRunsTheMocks tests a generated contract mock.
//
// Why this test is important:
//   - The code under test is handed Contract(); each of its operations and roles must
//     reach the matching mock, including the operations of an embedded struct, or a
//     test's expectations would silently not apply.
//
// What it tests:
//   - Find (embedded), Count and Get on Contract() return what their mocks were told to.
func TestOpmockgen_ContractRunsTheMocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := opmockgen.NewMockNotes(gomock.NewController(t))
	m.Find.EXPECT().Execute(gomock.Any(), "todo").Return(7, nil)
	m.Count.EXPECT().Execute(gomock.Any(), struct{}{}).Return(3, nil)
	title := "todo"
	m.Reader.EXPECT().Get(gomock.Any(), 7).Return(&title, nil)

	notes := m.Contract()
	id, err := notes.Find.Execute(ctx, "todo")
	require.NoError(t, err)
	assert.Equal(t, 7, id)
	n, err := notes.Count.Execute(ctx, struct{}{})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	got, err := notes.Get(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, "todo", *got)
}
