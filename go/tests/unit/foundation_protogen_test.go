package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/protogen"
)

// protogenPkg is the generated Python package the passes run over.
const protogenPkg = "acme_generated"

// writeProtoFile writes content to dir/name, creating parent dirs, failing the test on error.
func writeProtoFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// requiredBehavior returns FieldOptions carrying field_behavior = REQUIRED.
func requiredBehavior() *descriptorpb.FieldOptions {
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, annotations.E_FieldBehavior,
		[]annotations.FieldBehavior{annotations.FieldBehavior_REQUIRED})
	return opts
}

// msgField builds a singular message-typed field descriptor.
func msgField(
	name string,
	opts *descriptorpb.FieldOptions,
) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     new(name),
		JsonName: new(name),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Options:  opts,
	}
}

// TestRequiredMessageFields tests that only singular message fields marked REQUIRED are
// collected, keyed by fully-qualified message name and camelCase (json_name) property.
//
// Why this test is important:
//   - This set drives which generated properties get de-optionalized. A scalar, a
//     repeated field, or an unannotated field wrongly included would flip a field the
//     wire may omit; a REQUIRED message field missed would leave the `| undefined` the
//     change exists to remove.
//
// What it tests:
//   - a REQUIRED message field is collected under "pkg.Msg" by its json_name
//   - a REQUIRED scalar and a REQUIRED repeated message field are excluded
//   - an unannotated message field is excluded
func TestRequiredMessageFields(t *testing.T) {
	repeated := msgField("items", requiredBehavior())
	repeated.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	scalar := &descriptorpb.FieldDescriptorProto{
		Name: new("message_id"), JsonName: new("messageId"),
		Type:    descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Options: requiredBehavior(),
	}
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Package: new("ws.v1"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("ErrorMessage"),
			Field: []*descriptorpb.FieldDescriptorProto{
				msgField("error", requiredBehavior()), // REQUIRED message → kept
				msgField("conversation_id", nil),      // unannotated → skipped
				repeated,                              // REQUIRED repeated → skipped
				scalar,                                // REQUIRED scalar → skipped
			},
		}},
	}}}

	got := protogen.RequiredMessageFields(fds)

	require.Equal(t, map[string]map[string]struct{}{
		"ws.v1.ErrorMessage": {"error": {}},
	}, got)
}

// TestDeoptionalizeTSFields tests that the TS rewrite promotes exactly the required
// properties to non-optional, scoped by the enclosing message.
//
// Why this test is important:
//   - The rewrite edits gitignored generated output; over-reaching (touching an optional
//     or a same-named field in another message) or under-reaching would silently
//     misrepresent the wire contract the frontend compiles against.
//
// What it tests:
//   - a required property `name?: T | undefined` becomes `name: T`
//   - a non-required optional property in the same message is left optional
//   - a same-named property in a DIFFERENT (not-required) message is left optional
func TestDeoptionalizeTSFields(t *testing.T) {
	tsDir := t.TempDir()
	path := writeProtoFile(
		t,
		tsDir,
		"ws/v1/envelope_pb.ts",
		`export type ErrorMessage = Message<"ws.v1.ErrorMessage"> & {
  /**
   * @generated from field: optional string conversation_id = 1;
   */
  conversationId?: string | undefined;

  /**
   * @generated from field: common.v1.Error error = 2;
   */
  error?: Error | undefined;
};

export type Other = Message<"ws.v1.Other"> & {
  /**
   * @generated from field: common.v1.Error error = 1;
   */
  error?: Error | undefined;
};
`,
	)

	required := map[string]map[string]struct{}{
		"ws.v1.ErrorMessage": {"error": {}},
	}
	require.NoError(t, protogen.DeoptionalizeTSFields(tsDir, required))

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(out)

	require.Equal(t, 1, strings.Count(got, "  error: Error;"),
		"only ErrorMessage.error is de-optionalized")
	require.Equal(t, 1, strings.Count(got, "  error?: Error | undefined;"),
		"Other.error is left optional — the rewrite is message-scoped")
	require.Contains(t, got, "conversationId?: string | undefined;",
		"a non-required optional field is left untouched")
}

// TestDeoptionalizeTSFields_NoMatchIsNoop tests that a file with no required properties
// is left byte-for-byte unchanged.
//
// Why this test is important:
//   - The pass runs on every proto generation; a spurious rewrite would churn the tree
//     and could corrupt correct types. A true no-op when nothing matches is the safety
//     net.
//
// What it tests:
//   - With an empty required set, the _pb.ts content is identical before and after.
func TestDeoptionalizeTSFields_NoMatchIsNoop(t *testing.T) {
	tsDir := t.TempDir()
	original := `export type Wrap = Message<"a.v1.Wrap"> & {
  error?: Error | undefined;
};
`
	path := writeProtoFile(t, tsDir, "a/v1/a_pb.ts", original)

	require.NoError(
		t,
		protogen.DeoptionalizeTSFields(tsDir, map[string]map[string]struct{}{}),
	)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(out))
}

// TestEnsurePythonInitFiles tests that __init__.py files are created in all Python
// package directories.
//
// Why this test is important:
//   - Python requires __init__.py for directory-based package imports
//   - Generated protobuf output directories need these files to be importable
//   - Missing __init__.py causes runtime ImportErrors in downstream services
//
// What it tests:
//   - __init__.py is created in every directory under the generated Python package
//   - Created __init__.py files are empty (zero bytes)
//   - Function succeeds without errors on a valid directory tree
func TestEnsurePythonInitFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pyDir := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
		"common",
		"v1",
	)
	require.NoError(t, os.MkdirAll(pyDir, 0o755))

	// Pre-check: no __init__.py files exist.
	_, err := os.Stat(
		filepath.Join(
			root,
			"gen",
			"python",
			protogenPkg,
			"src",
			protogenPkg,
			"__init__.py",
		),
	)
	assert.True(t, os.IsNotExist(err))

	// Run the function.
	err = protogen.EnsurePythonInitFiles(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	// Verify __init__.py was created in every directory.
	base := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
	)
	for _, dir := range []string{
		base,
		filepath.Join(base, "common"),
		filepath.Join(base, "common", "v1"),
	} {
		initFile := filepath.Join(dir, "__init__.py")
		info, statErr := os.Stat(initFile)
		require.NoError(t, statErr, "expected __init__.py in %s", dir)
		assert.Equal(t, int64(0), info.Size(), "expected empty __init__.py")
	}
}

// TestRewritePythonImports tests that bare Python imports in _pb2.py files are rewritten
// with the gen prefix.
//
// Why this test is important:
//   - Protobuf-generated Python files use bare imports that break in the monorepo layout
//   - Import rewriting is essential for Python services to resolve protobuf dependencies
//   - Must selectively rewrite only bare imports, not google or already-prefixed imports
//
// What it tests:
//   - Bare cross-package import is rewritten with acme_generated prefix
//   - Google protobuf imports are left unchanged
//   - Already-prefixed imports are left unchanged
func TestRewritePythonImports(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(
		root, "gen", "python", protogenPkg, "src", protogenPkg,
	)
	pb2Dir := filepath.Join(base, "workspace", "v1")
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))
	// The referenced sibling proto package must exist as a subdirectory of the
	// generated root for its import to be qualified.
	require.NoError(t, os.MkdirAll(filepath.Join(base, "common", "v1"), 0o755))

	// Create a fake _pb2.py with bare import.
	content := `# Generated by protoc.
from common.v1 import common_pb2
from google.protobuf import descriptor_pb2
from acme_generated.already.prefixed import something_pb2
`
	testFile := filepath.Join(pb2Dir, "workspace_pb2.py")
	require.NoError(t, os.WriteFile(testFile, []byte(content), 0o644))

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	got, err := os.ReadFile(testFile)
	require.NoError(t, err)

	result := string(got)
	// The bare import should be rewritten.
	assert.Contains(t, result, "from acme_generated.common.v1 import common_pb2")
	// The google import should NOT be rewritten.
	assert.Contains(t, result, "from google.protobuf import descriptor_pb2")
	// The already-prefixed import should NOT be rewritten.
	assert.Contains(
		t,
		result,
		"from acme_generated.already.prefixed import something_pb2",
	)
}

// TestRewritePythonImports_NonPb2FilesIgnored tests that non-protobuf Python files are
// not modified by import rewriting.
//
// Why this test is important:
//   - Only generated _pb2.py files should be rewritten; other Python files must be
//     preserved
//   - Accidentally rewriting helper or user-authored files would introduce bugs
//
// What it tests:
//   - A regular .py file (not _pb2.py) with bare imports is left unchanged
func TestRewritePythonImports_NonPb2FilesIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pb2Dir := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
		"common",
	)
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))

	// A regular Python file (not _pb2.py) should be left alone.
	content := "from common.v1 import common_pb2\n"
	regularFile := filepath.Join(pb2Dir, "helper.py")
	require.NoError(t, os.WriteFile(regularFile, []byte(content), 0o644))

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	got, err := os.ReadFile(regularFile)
	require.NoError(t, err)
	assert.Equal(t, content, string(got), "non _pb2.py file should be unchanged")
}

// TestEnsurePythonInitFiles_AlreadyExists tests that existing __init__.py files are not
// overwritten.
//
// Why this test is important:
//   - Users or prior runs may have customized __init__.py files
//   - The function must be idempotent and not destroy existing content
//
// What it tests:
//   - Pre-existing __init__.py with content is preserved unchanged
//   - Function completes without error
func TestEnsurePythonInitFiles_AlreadyExists(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pyDir := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
	)
	require.NoError(t, os.MkdirAll(pyDir, 0o755))

	// Pre-create __init__.py.
	initFile := filepath.Join(pyDir, "__init__.py")
	require.NoError(t, os.WriteFile(initFile, []byte("# existing\n"), 0o644))

	err := protogen.EnsurePythonInitFiles(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	// Verify existing file was NOT overwritten.
	content, err := os.ReadFile(initFile)
	require.NoError(t, err)
	assert.Equal(t, "# existing\n", string(content))
}

// TestEnsurePythonInitFiles_EmptyDir tests that __init__.py is created in an empty Python
// directory.
//
// Why this test is important:
//   - Even the root gen/python/ directory needs __init__.py for Python package resolution
//   - Edge case where there are no subdirectories must still work
//
// What it tests:
//   - __init__.py is created in gen/python/ with zero size
//   - Function completes without error
func TestEnsurePythonInitFiles_EmptyDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Create just the generated package dir with no subdirs.
	pyDir := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
	)
	require.NoError(t, os.MkdirAll(pyDir, 0o755))

	err := protogen.EnsurePythonInitFiles(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	// Verify __init__.py created in the python root.
	info, err := os.Stat(filepath.Join(pyDir, "__init__.py"))
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())
}

// TestEnsurePythonInitFiles_DirNotExist tests that EnsurePythonInitFiles returns an error
// when the directory is missing.
//
// Why this test is important:
//   - If gen/python/ does not exist, the function cannot walk the directory tree
//   - Missing directory errors must be reported rather than silently ignored
//
// What it tests:
//   - Error is returned when gen/python/ does not exist
func TestEnsurePythonInitFiles_DirNotExist(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Do NOT create gen/python/ — WalkDir should fail.

	err := protogen.EnsurePythonInitFiles(
		root,
		protogenPkg,
	)
	require.Error(t, err)
}

// TestEnsurePythonInitFiles_DeeplyNested tests that __init__.py files are created at
// every level of a deep directory tree.
//
// Why this test is important:
//   - Protobuf packages can produce deeply nested directory structures
//   - Every intermediate directory must have __init__.py for Python package resolution
//
// What it tests:
//   - __init__.py exists in all six directory levels from gen/python/ to pb2/a/b/c/d
func TestEnsurePythonInitFiles_DeeplyNested(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
	)
	deepDir := filepath.Join(base, "a", "b", "c", "d")
	require.NoError(t, os.MkdirAll(deepDir, 0o755))

	err := protogen.EnsurePythonInitFiles(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	// Check __init__.py in every level.
	for _, dir := range []string{
		base,
		filepath.Join(base, "a"),
		filepath.Join(base, "a", "b"),
		filepath.Join(base, "a", "b", "c"),
		filepath.Join(base, "a", "b", "c", "d"),
	} {
		_, err := os.Stat(filepath.Join(dir, "__init__.py"))
		require.NoError(t, err, "missing __init__.py in %s", dir)
	}
}

// ===========================================================================
// RewritePythonImports tests — additional coverage
// ===========================================================================

// TestRewritePythonImports_GrpcFile tests that _pb2_grpc.py files also have their imports
// rewritten.
//
// Why this test is important:
//   - gRPC service stubs (_pb2_grpc.py) also contain bare cross-package imports
//   - Both _pb2.py and _pb2_grpc.py files must be processed for correct Python imports
//
// What it tests:
//   - Bare imports in _pb2_grpc.py files are rewritten with acme_generated prefix
//   - Multiple imports in the same file are all rewritten
func TestRewritePythonImports_GrpcFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(
		root, "gen", "python", protogenPkg, "src", protogenPkg,
	)
	pb2Dir := filepath.Join(base, "query", "v1")
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))
	// The referenced sibling proto packages must exist as subdirectories of the
	// generated root for their imports to be qualified.
	require.NoError(t, os.MkdirAll(filepath.Join(base, "common", "v1"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "retrieval", "v1"), 0o755))

	// Test _pb2_grpc.py files are also rewritten.
	content := `# Generated by protoc.
from common.v1 import common_pb2
from retrieval.v1 import retrieval_pb2
`
	testFile := filepath.Join(pb2Dir, "query_pb2_grpc.py")
	require.NoError(t, os.WriteFile(testFile, []byte(content), 0o644))

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	got, err := os.ReadFile(testFile)
	require.NoError(t, err)

	result := string(got)
	assert.Contains(t, result, "from acme_generated.common.v1 import common_pb2")
	assert.Contains(
		t,
		result,
		"from acme_generated.retrieval.v1 import retrieval_pb2",
	)
}

// TestRewritePythonImports_NoChangesNeeded tests that files needing no rewrites are left
// unchanged.
//
// Why this test is important:
//   - Idempotent behavior prevents unnecessary file writes and git churn
//   - Files with only google or already-prefixed imports must not be modified
//
// What it tests:
//   - File content is identical before and after rewriting when no changes are needed
func TestRewritePythonImports_NoChangesNeeded(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pb2Dir := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
		"common",
		"v1",
	)
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))

	// File where all imports are already prefixed or google imports.
	content := `# Generated by protoc.
from google.protobuf import descriptor_pb2
from acme_generated.common.v1 import common_pb2
import os
x = 42
`
	testFile := filepath.Join(pb2Dir, "common_pb2.py")
	require.NoError(t, os.WriteFile(testFile, []byte(content), 0o644))

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	got, err := os.ReadFile(testFile)
	require.NoError(t, err)
	// Should be unchanged.
	assert.Equal(t, content, string(got))
}

// TestRewritePythonImports_EmptyPb2Dir tests that rewriting succeeds with an empty pb2
// directory.
//
// Why this test is important:
//   - An empty pb2 directory is valid when no proto files have been compiled yet
//   - The function must handle the empty case without error
//
// What it tests:
//   - Function completes without error when the generated Python directory is empty
func TestRewritePythonImports_EmptyPb2Dir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pb2Dir := filepath.Join(
		root,
		"gen",
		"python",
		protogenPkg,
		"src",
		protogenPkg,
	)
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.NoError(t, err)
}

// TestRewritePythonImports_DirNotExist tests that RewritePythonImports returns an error
// when pb2 directory is missing.
//
// Why this test is important:
//   - Missing pb2 directory indicates buf generate was not run or failed
//   - Error must be surfaced so the caller knows import rewriting cannot proceed
//
// What it tests:
//   - Error is returned when the generated Python directory does not exist
func TestRewritePythonImports_DirNotExist(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Do NOT create the generated Python directory — WalkDir should fail.

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.Error(t, err)
}

// TestRewritePythonImports_MultipleRewritesInOneFile tests that all bare imports in a
// single file are rewritten.
//
// Why this test is important:
//   - Proto files commonly import from multiple packages simultaneously
//   - All bare imports in a file must be rewritten in a single pass
//
// What it tests:
//   - Three bare cross-package imports are all rewritten with acme_generated prefix
//   - Google protobuf import in the same file is left unchanged
func TestRewritePythonImports_MultipleRewritesInOneFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(
		root, "gen", "python", protogenPkg, "src", protogenPkg,
	)
	pb2Dir := filepath.Join(base, "query", "v1")
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))
	// The referenced sibling proto packages must exist as subdirectories of the
	// generated root for their imports to be qualified.
	for _, pkg := range []string{"common", "workspace", "retrieval"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, pkg, "v1"), 0o755))
	}

	content := `# Generated by protoc.
from common.v1 import common_pb2
from workspace.v1 import workspace_pb2
from retrieval.v1 import retrieval_pb2
from google.protobuf import descriptor_pb2
`
	testFile := filepath.Join(pb2Dir, "query_pb2.py")
	require.NoError(t, os.WriteFile(testFile, []byte(content), 0o644))

	err := protogen.RewritePythonImports(
		root,
		protogenPkg,
	)
	require.NoError(t, err)

	got, err := os.ReadFile(testFile)
	require.NoError(t, err)
	result := string(got)

	assert.Contains(t, result, "from acme_generated.common.v1 import common_pb2")
	assert.Contains(
		t,
		result,
		"from acme_generated.workspace.v1 import workspace_pb2",
	)
	assert.Contains(
		t,
		result,
		"from acme_generated.retrieval.v1 import retrieval_pb2",
	)
	assert.Contains(t, result, "from google.protobuf import descriptor_pb2")
}

// ===========================================================================
// Client tests — additional coverage
// ===========================================================================

// TestRewritePythonImports_PyiFile tests that _pb2.pyi / _pb2_grpc.pyi type stubs
// also have their bare cross-package imports package-qualified.
//
// Why this test is important:
//   - basedpyright resolves proto message + service types for downstream consumers
//     from the generated .pyi stubs; if those stubs keep bare `from common.v1 import
//     ...` imports the checker cannot resolve, every cross-package proto symbol reads
//     as Unknown and strict mode drowns in false findings.
//
// What it tests:
//   - A bare cross-package import in a _pb2.pyi is rewritten with the
//     acme_generated. prefix, exactly like the _pb2.py path.
//   - A _pb2_grpc.pyi stub is rewritten too.
func TestRewritePythonImports_PyiFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(
		root, "gen", "python", protogenPkg, "src", protogenPkg,
	)
	pb2Dir := filepath.Join(base, "query", "v1")
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))
	// The referenced sibling proto packages must exist as subdirectories of the
	// generated root for their imports to be qualified.
	require.NoError(t, os.MkdirAll(filepath.Join(base, "common", "v1"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "retrieval", "v1"), 0o755))

	msgStub := filepath.Join(pb2Dir, "query_pb2.pyi")
	require.NoError(
		t,
		os.WriteFile(msgStub, []byte("from common.v1 import validation_pb2\n"), 0o644),
	)
	grpcStub := filepath.Join(pb2Dir, "query_pb2_grpc.pyi")
	require.NoError(
		t,
		os.WriteFile(grpcStub, []byte("from retrieval.v1 import retrieval_pb2\n"), 0o644),
	)

	require.NoError(
		t,
		protogen.RewritePythonImports(
			root,
			protogenPkg,
		),
	)

	gotMsg, err := os.ReadFile(msgStub)
	require.NoError(t, err)
	assert.Contains(
		t, string(gotMsg), "from acme_generated.common.v1 import validation_pb2",
	)
	gotGrpc, err := os.ReadFile(grpcStub)
	require.NoError(t, err)
	assert.Contains(
		t, string(gotGrpc), "from acme_generated.retrieval.v1 import retrieval_pb2",
	)
}

// TestRewritePythonImports_PyiSkipsStdlibAndThirdParty tests that stdlib and
// third-party imports in the mypy-generated .pyi stubs are left untouched, while a
// real cross-package proto import is still package-qualified.
//
// Why this test is important:
//   - The grpc mypy .pyi stubs pull in stdlib/third-party modules with the same
//     `from X import Y` shape as a cross-package proto import (from collections
//     import abc, from grpc import aio, from typing import ClassVar). Blindly
//     prefixing every such line with acme_generated. produces broken imports
//     (from acme_generated.grpc import aio) that basedpyright cannot resolve —
//     the exact regression the qualified-only-proto-packages rule prevents.
//
// What it tests:
//   - collections/grpc/typing imports are unchanged (they are not proto packages).
//   - A from common.v1 import ... line IS qualified, because common is a real proto
//     package subdirectory of the generated root.
//   - A google.protobuf import stays untouched.
func TestRewritePythonImports_PyiSkipsStdlibAndThirdParty(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(
		root, "gen", "python", protogenPkg, "src", protogenPkg,
	)
	// The generated tree carries real proto-package subdirectories; only these are
	// qualified. collections / grpc / typing are NOT subdirectories, so they stay.
	pb2Dir := filepath.Join(base, "query", "v1")
	require.NoError(t, os.MkdirAll(pb2Dir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "common", "v1"), 0o755))

	grpcStub := filepath.Join(pb2Dir, "query_pb2_grpc.pyi")
	content := `from collections import abc as _abc
from collections.abc import Iterable as _Iterable
from grpc import aio as _aio
from typing import ClassVar
from common.v1 import validation_pb2 as _validation_pb2
from google.protobuf import descriptor_pb2 as _descriptor_pb2
`
	require.NoError(t, os.WriteFile(grpcStub, []byte(content), 0o644))

	require.NoError(
		t,
		protogen.RewritePythonImports(
			root,
			protogenPkg,
		),
	)

	got, err := os.ReadFile(grpcStub)
	require.NoError(t, err)
	result := string(got)

	// Stdlib / third-party imports are left exactly as-is.
	assert.Contains(t, result, "from collections import abc as _abc")
	assert.Contains(t, result, "from collections.abc import Iterable as _Iterable")
	assert.Contains(t, result, "from grpc import aio as _aio")
	assert.Contains(t, result, "from typing import ClassVar")
	assert.NotContains(t, result, "acme_generated.collections")
	assert.NotContains(t, result, "acme_generated.grpc")
	assert.NotContains(t, result, "acme_generated.typing")

	// The real cross-package proto import is qualified; google stays untouched.
	assert.Contains(
		t,
		result,
		"from acme_generated.common.v1 import validation_pb2 as _validation_pb2",
	)
	assert.Contains(
		t,
		result,
		"from google.protobuf import descriptor_pb2 as _descriptor_pb2",
	)
}

// TestImportPrefixRe tests that the Python import prefix regex correctly matches and
// captures cross-package imports.
//
// Why this test is important:
//   - The regex drives automatic import rewriting in generated protobuf Python code
//   - Incorrect regex would silently produce broken imports in generated code
//   - Must distinguish valid package imports from comments, bare imports, and
//     already-prefixed imports
//
// What it tests:
//   - Bare cross-package imports match and capture correct groups
//   - Single, nested, and multi-segment package names are captured
//   - Google imports and already-prefixed imports match the regex (filtered by caller)
//   - Non-import lines, comments, empty lines, uppercase, and digit-prefixed packages do
//     not match
func TestImportPrefixRe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		line   string
		groups []string
		match  bool
	}{
		{
			name:  "bare cross-package import",
			line:  "from common.v1 import common_pb2",
			match: true,
			groups: []string{
				"from common.v1 import common_pb2",
				"from ",
				"common.v1",
				" import common_pb2",
			},
		},
		{
			name:  "single package import",
			line:  "from workspace import workspace_pb2",
			match: true,
			groups: []string{
				"from workspace import workspace_pb2",
				"from ",
				"workspace",
				" import workspace_pb2",
			},
		},
		{
			name:  "nested package import",
			line:  "from retrieval.v1.types import something",
			match: true,
			groups: []string{
				"from retrieval.v1.types import something",
				"from ",
				"retrieval.v1.types",
				" import something",
			},
		},
		{
			name:  "google import (matches regex, filtered by caller)",
			line:  "from google.protobuf import descriptor_pb2",
			match: true,
		},
		{
			name:  "already prefixed import (matches regex, filtered by caller)",
			line:  "from acme_generated.common.v1 import common_pb2",
			match: true,
		},
		{
			name:  "import statement (no from)",
			line:  "import common_pb2",
			match: false,
		},
		{
			name:  "comment line",
			line:  "# from common.v1 import common_pb2",
			match: false,
		},
		{
			name:  "empty line",
			line:  "",
			match: false,
		},
		{
			name:  "uppercase package",
			line:  "from Common.v1 import foo",
			match: false,
		},
		{
			name:  "package starting with digit",
			line:  "from 1common import foo",
			match: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := protogen.ImportPrefixRe.FindStringSubmatch(tt.line)
			if tt.match {
				assert.NotNil(t, m, "expected %q to match", tt.line)
				if tt.groups != nil {
					assert.Equal(t, tt.groups, m)
				}
			} else {
				assert.Nil(t, m, "expected %q not to match", tt.line)
			}
		})
	}
}

// TestImportPrefixRe_SingleSegmentPackage tests that the regex captures single-segment
// package names with aliases.
//
// Why this test is important:
//   - Some proto packages have single-segment names without version suffixes
//   - Import aliases (as ws) must not break the regex match
//
// What it tests:
//   - Single-segment package name "workspace" is captured in group 2
func TestImportPrefixRe_SingleSegmentPackage(t *testing.T) {
	t.Parallel()

	line := "from workspace import workspace_pb2 as ws"
	m := protogen.ImportPrefixRe.FindStringSubmatch(line)
	require.NotNil(t, m)
	assert.Equal(t, "workspace", m[2])
}

// TestImportPrefixRe_DeepNesting tests that the regex captures deeply nested package
// paths.
//
// Why this test is important:
//   - Proto packages can be deeply nested (e.g., a.b.c.d.e)
//   - The regex must capture the full dotted path as a single group
//
// What it tests:
//   - Five-segment dotted package path is captured in group 2
func TestImportPrefixRe_DeepNesting(t *testing.T) {
	t.Parallel()

	line := "from a.b.c.d.e import something"
	m := protogen.ImportPrefixRe.FindStringSubmatch(line)
	require.NotNil(t, m)
	assert.Equal(t, "a.b.c.d.e", m[2])
}

// TestImportPrefixRe_NumericInPackageName tests that the regex allows digits within
// package names.
//
// Why this test is important:
//   - Package names like "common2" contain digits after the first character
//   - The regex must accept alphanumeric package names starting with a letter
//
// What it tests:
//   - Package name "common2.v1" with embedded digits is captured in group 2
func TestImportPrefixRe_NumericInPackageName(t *testing.T) {
	t.Parallel()

	line := "from common2.v1 import common_pb2"
	m := protogen.ImportPrefixRe.FindStringSubmatch(line)
	require.NotNil(t, m)
	assert.Equal(t, "common2.v1", m[2])
}

// TestImportPrefixRe_UnderscoreInPackageName tests that the regex allows underscores in
// package names.
//
// Why this test is important:
//   - Python package names commonly use underscores (PEP 8 convention)
//   - The regex must accept underscored package names
//
// What it tests:
//   - Package name "my_package.v1" with underscores is captured in group 2
func TestImportPrefixRe_UnderscoreInPackageName(t *testing.T) {
	t.Parallel()

	line := "from my_package.v1 import my_pb2"
	m := protogen.ImportPrefixRe.FindStringSubmatch(line)
	require.NotNil(t, m)
	assert.Equal(t, "my_package.v1", m[2])
}

// ===========================================================================
// Diagrams tests — coverage from 0% to as high as possible
// ===========================================================================

// TestEnsurePythonTypedMarker_CreatesMarker tests that a PEP 561 py.typed marker is
// written at the root of the generated package.
//
// Why this test is important:
//   - Without a py.typed marker basedpyright treats the editable-installed
//     acme_generated as untyped and reads none of its .py/.pyi stubs, so proto
//     types never resolve for consumers no matter how well the stubs are qualified.
//
// What it tests:
//   - An empty py.typed file exists under src/<GeneratedPythonPkg> after the helper runs.
func TestEnsurePythonTypedMarker_CreatesMarker(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pyDir := filepath.Join(
		root, "gen", "python", protogenPkg, "src", protogenPkg,
	)
	require.NoError(t, os.MkdirAll(pyDir, 0o755))

	require.NoError(
		t,
		protogen.EnsurePythonTypedMarker(
			root,
			protogenPkg,
		),
	)

	info, err := os.Stat(filepath.Join(pyDir, "py.typed"))
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())
}
