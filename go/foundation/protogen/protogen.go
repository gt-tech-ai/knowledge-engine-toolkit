// Package protogen holds the file passes a proto generation runs over buf's output: the
// generated Python package's skeleton (__init__.py files, the py.typed marker) and its
// package-qualified imports, the protobuf-es REQUIRED-field promotion read from a
// descriptor set, and the clean / stash-and-settle moves that regenerate a directory
// without losing its committed contents on a failed run.
package protogen

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// ImportPrefixRe matches "from <bare_package> import ..." lines in generated
// Python protobuf files.
var ImportPrefixRe = regexp.MustCompile(
	`^(from )([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)( import .*)$`,
)

// EnsurePythonInitFiles walks the generated Python package directory and
// creates an empty __init__.py in every directory that lacks one.
func EnsurePythonInitFiles(root, genPkg string) error {
	pyDir := filepath.Join(root, "gen", "python", genPkg, "src", genPkg)
	return walkRoot(pyDir, func(r *os.Root, path string, d fs.DirEntry) error {
		if !d.IsDir() {
			return nil
		}
		initFile := filepath.Join(path, "__init__.py")
		if _, statErr := r.Stat(initFile); os.IsNotExist(statErr) {
			if err := writeGenerated(r, initFile, []byte{}); err != nil {
				return err
			}
		}
		return nil
	})
}

// walkRoot walks dir through an os.Root scoped to it, calling fn with that root and each
// entry's dir-relative path, so a symlink swapped in mid-walk cannot redirect a read or
// write outside dir. Every error is CodeInternal.
func walkRoot(dir string, fn func(r *os.Root, path string, d fs.DirEntry) error) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open "+dir)
	}
	defer func() { _ = r.Close() }()
	err = fs.WalkDir(r.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return fn(r, path, d)
	})
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "walk "+dir)
	}
	return nil
}

// writeGenerated writes a generated source file under r, world-readable like the
// checked-out tree it joins (the images and editable installs read it as other users).
func writeGenerated(r *os.Root, path string, data []byte) error {
	if err := r.WriteFile(path, data, 0o644); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "write "+path)
	}
	return nil
}

// EnsurePythonTypedMarker writes an empty PEP 561 py.typed marker at the root of
// the generated Python package, so basedpyright treats the editable-installed
// generated package as a typed package and reads its .py/.pyi stubs when resolving
// proto types for downstream consumers. It is idempotent (an existing marker is
// left as-is by the WriteFile overwrite of empty content).
func EnsurePythonTypedMarker(root, genPkg string) error {
	pkgDir := filepath.Join(root, "gen", "python", genPkg, "src", genPkg)
	r, err := os.OpenRoot(pkgDir)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open "+pkgDir)
	}
	defer func() { _ = r.Close() }()
	return writeGenerated(r, "py.typed", []byte{})
}

var (
	// tsMessageTypeRe captures the fully-qualified proto message name a generated `export
	// type X = Message<"pkg.X">` block models, so a rewrite is scoped to the right
	// message.
	tsMessageTypeRe = regexp.MustCompile(`Message<"([^"]+)">`)
	// tsOptionalPropRe matches a generated optional property line (` name?: T |
	// undefined;`), capturing indent, the (camelCase) property name, and the type for the
	// rewrite.
	tsOptionalPropRe = regexp.MustCompile(`^(\s*)(\w+)\?:\s*(.+?)\s*\|\s*undefined;\s*$`)
)

// ReadDescriptorSet reads the google.protobuf.FileDescriptorSet `buf build
// --as-file-descriptor-set` wrote at path, so field options (field_behavior) are read as
// structured data — the REQUIRED set DeoptionalizeTSFields promotes. An empty file is an
// empty set.
func ReadDescriptorSet(path string) (*descriptorpb.FileDescriptorSet, error) {
	data, err := os.ReadFile(
		path,
	)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "read descriptor set")
	}
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &fds); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "unmarshal descriptor set")
	}
	return &fds, nil
}

// RequiredMessageFields returns, per fully-qualified message name, the set of camelCase
// field names (the protobuf-es property names, i.e. json_name) that are singular message
// fields marked field_behavior = REQUIRED — exactly the fields protoc-gen-es types
// `?: T | undefined` but the contract guarantees present.
func RequiredMessageFields(
	fds *descriptorpb.FileDescriptorSet,
) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	var walk func(prefix string, m *descriptorpb.DescriptorProto)
	walk = func(prefix string, m *descriptorpb.DescriptorProto) {
		full := prefix + "." + m.GetName()
		for _, f := range m.GetField() {
			if isRequiredMessageField(f) {
				if out[full] == nil {
					out[full] = map[string]struct{}{}
				}
				out[full][f.GetJsonName()] = struct{}{}
			}
		}
		for _, nested := range m.GetNestedType() {
			walk(full, nested)
		}
	}
	for _, file := range fds.GetFile() {
		for _, m := range file.GetMessageType() {
			walk(file.GetPackage(), m)
		}
	}
	return out
}

// isRequiredMessageField reports whether f is a singular (non-repeated) message field
// carrying field_behavior = REQUIRED. Scalar/enum fields already generate non-optional,
// and repeated fields generate arrays, so neither needs the rewrite.
func isRequiredMessageField(f *descriptorpb.FieldDescriptorProto) bool {
	if f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE ||
		f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED ||
		f.GetOptions() == nil {
		return false
	}
	behaviors, ok := proto.GetExtension(
		f.GetOptions(), annotations.E_FieldBehavior,
	).([]annotations.FieldBehavior)
	return ok && slices.Contains(behaviors, annotations.FieldBehavior_REQUIRED)
}

// DeoptionalizeTSFields rewrites every `_pb.ts` under tsDir, turning `name?: T |
// undefined` into `name: T` for each property whose enclosing message + name is in
// required. Files are written only when a property actually changed.
func DeoptionalizeTSFields(tsDir string, required map[string]map[string]struct{}) error {
	return walkRoot(tsDir, func(r *os.Root, path string, d fs.DirEntry) error {
		if d.IsDir() || !strings.HasSuffix(path, "_pb.ts") {
			return nil
		}
		data, readErr := r.ReadFile(path)
		if readErr != nil {
			return apperr.Wrap(readErr, apperr.CodeInternal, "read "+path)
		}
		lines := strings.Split(string(data), "\n")
		curMsg := ""
		changed := false
		for i, line := range lines {
			if m := tsMessageTypeRe.FindStringSubmatch(line); m != nil {
				curMsg = m[1]
				continue
			}
			m := tsOptionalPropRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if _, ok := required[curMsg][m[2]]; ok {
				lines[i] = m[1] + m[2] + ": " + m[3] + ";"
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return writeGenerated(r, path, []byte(strings.Join(lines, "\n")))
	})
}

// CleanGenDir removes everything in dir except go.mod and go.sum, preserving
// the module identity so that go mod tidy under go.work stays deterministic.
func CleanGenDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return mkdirAll(dir)
	}
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "read "+dir)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "go.mod" || name == "go.sum" {
			continue
		}
		if err := removeAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// StashDir moves dir's current contents aside and leaves dir empty for a generator to
// fill, returning the backup directory SettleDir takes. The backup sits beside dir so
// the moves are same-filesystem renames; a missing dir stashes nothing.
func StashDir(dir string) (string, error) {
	parent := filepath.Dir(dir)
	if err := mkdirAll(parent); err != nil {
		return "", err
	}
	backup, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+"-prev-")
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "create the backup of "+dir)
	}
	if err := os.Rename(dir, filepath.Join(backup, stashed)); err != nil &&
		!os.IsNotExist(err) {
		_ = os.RemoveAll(backup)
		return "", apperr.Wrap(err, apperr.CodeInternal, "stash "+dir)
	}
	return backup, mkdirAll(dir)
}

// SettleDir ends a StashDir: on restore (the generator failed) it puts the stashed
// contents back in place of whatever the generator left, so a failed generator (a buf or
// BSR error) never leaves committed generated output deleted; either way it removes the
// backup. An empty backup is a no-op.
func SettleDir(dir, backup string, restore bool) error {
	if backup == "" {
		return nil
	}
	prev := filepath.Join(backup, stashed)
	if _, err := os.Stat(prev); restore && err == nil {
		if err := removeAll(dir); err != nil {
			return err
		}
		if err := os.Rename(prev, dir); err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "restore "+dir)
		}
	}
	return removeAll(backup)
}

// mkdirAll creates dir and its parents, world-readable like the checked-out tree; a
// failure is CodeInternal.
func mkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create "+dir)
	}
	return nil
}

// removeAll removes path and anything under it; a failure is CodeInternal.
func removeAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "remove "+path)
	}
	return nil
}

// stashed is the name StashDir moves a directory's contents to inside its backup.
const stashed = "contents"

// topLevelProtoPackages returns the set of immediate subdirectory names of the
// generated package root. These are exactly the proto packages emitted into the
// generated package (common, identity, buf, …); a `from <pkg> import …` line is
// package-qualified only when its first path segment names one of them, so the
// stdlib and third-party modules the mypy `.pyi` stubs pull in (collections,
// typing, grpc) — which share the same import shape — are left untouched.
func topLevelProtoPackages(pb2Dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(pb2Dir)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "read "+pb2Dir)
	}
	pkgs := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			pkgs[e.Name()] = struct{}{}
		}
	}
	return pkgs, nil
}

// RewritePythonImports walks the generated Python package directory and
// rewrites bare cross-package imports to use the fully-qualified path, in both
// the runtime `_pb2.py`/`_pb2_grpc.py` modules and their `_pb2.pyi`/`_pb2_grpc.pyi`
// type stubs (the latter so basedpyright resolves proto types for consumers).
// Only imports whose first path segment names a real proto-package subdirectory of
// the generated root are qualified; stdlib and third-party imports the mypy `.pyi`
// stubs carry (collections, typing, grpc) are left alone.
func RewritePythonImports(root, genPkg string) error {
	importPrefix := genPkg + "."
	pb2Dir := filepath.Join(root, "gen", "python", genPkg, "src", genPkg)
	protoPkgs, err := topLevelProtoPackages(pb2Dir)
	if err != nil {
		return err
	}
	return walkRoot(pb2Dir, func(r *os.Root, path string, d fs.DirEntry) error {
		if d.IsDir() || !isPythonProtoModule(d.Name()) {
			return nil
		}
		data, readErr := r.ReadFile(path)
		if readErr != nil {
			return apperr.Wrap(readErr, apperr.CodeInternal, "read "+path)
		}
		lines := strings.Split(string(data), "\n")
		if !qualifyImports(lines, importPrefix, protoPkgs) {
			return nil
		}
		return writeGenerated(r, path, []byte(strings.Join(lines, "\n")))
	})
}

// isPythonProtoModule reports whether name is a generated runtime module or type stub.
// Both the runtime .py and the .pyi type stubs carry the bare cross-package imports;
// the .pyi stubs must be rewritten too, or basedpyright cannot resolve proto types for
// downstream consumers.
func isPythonProtoModule(name string) bool {
	for _, suffix := range []string{"_pb2.py", "_pb2_grpc.py", "_pb2.pyi", "_pb2_grpc.pyi"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// qualifyImports rewrites, in place, each `from <pkg> import …` line whose first segment
// names one of protoPkgs to import from importPrefix+pkg, leaving google.* and
// already-qualified imports alone, and reports whether any line changed.
func qualifyImports(
	lines []string,
	importPrefix string,
	protoPkgs map[string]struct{},
) bool {
	changed := false
	for i, line := range lines {
		m := ImportPrefixRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pkg := m[2]
		if strings.HasPrefix(pkg, "google") || strings.HasPrefix(pkg, importPrefix) {
			continue
		}
		firstSeg, _, _ := strings.Cut(pkg, ".")
		if _, ok := protoPkgs[firstSeg]; !ok {
			continue
		}
		lines[i] = m[1] + importPrefix + pkg + m[3]
		changed = true
	}
	return changed
}
