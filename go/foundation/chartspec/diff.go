package chartspec

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	"github.com/gowebpki/jcs"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// maxTreeDepth bounds Diff's descent: the deepest legal path is the root, the filter
// root and types.MaxFilterDepth nested groups below it.
const maxTreeDepth = types.MaxFilterDepth + 2

// Change is one difference between two versions of a spec.
type Change struct {
	// Path locates the change from the root, dotted, with list positions in brackets:
	// "columns.args[0].agg" or "filter.members[1]". A root field has no prefix.
	Path string

	// Before is the node hash on the old side: for a changed field, the node that owns
	// it; for an added, removed or re-kinded child, the child itself (zero if absent).
	Before Hash

	// After is the node hash on the new side, with the same meaning as Before.
	After Hash
}

// differ accumulates the changes of one Diff call.
type differ struct {
	// dec fetches and verifies nodes.
	dec decoder
	// changes collects the differences found, in walk order.
	changes []Change
}

// Diff reports how the spec rooted at b differs from the one rooted at a, descending
// only where hashes differ: equal roots return no changes without a fetch. Fields,
// children and lists are walked in name order and list elements by position, so a
// reordered list reports each moved position. A zero side (no previous or no next
// version) is one root Change carrying both hashes, also without a fetch. Fetch failures
// carry Decode's codes.
func Diff(a, b Hash, get NodeGetter) ([]Change, error) {
	if a == b {
		return nil, nil
	}
	if a.IsZero() || b.IsZero() {
		return []Change{{Before: a, After: b}}, nil
	}
	d := differ{dec: decoder{get: get}}
	if err := d.node(a, b, "", 0); err != nil {
		return nil, err
	}
	return d.changes, nil
}

// join appends a member name to a path.
func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// node diffs two differing, non-zero nodes at path.
func (d *differ) node(a, b Hash, path string, depth int) error {
	if depth > maxTreeDepth {
		return apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: tree nesting exceeds max depth",
		)
	}
	na, err := d.dec.fetch(a)
	if err != nil {
		return err
	}
	nb, err := d.dec.fetch(b)
	if err != nil {
		return err
	}
	if na.Kind != nb.Kind {
		d.changes = append(d.changes, Change{Path: path, Before: a, After: b})
		return nil
	}
	if err := d.fields(na, nb, a, b, path); err != nil {
		return err
	}
	for _, name := range unionKeys(na.Children, nb.Children) {
		if err := d.child(
			na.Children[name],
			nb.Children[name],
			join(path, name),
			depth,
		); err != nil {
			return err
		}
	}
	for _, name := range unionKeys(na.Lists, nb.Lists) {
		la, lb := na.Lists[name], nb.Lists[name]
		for i := range max(len(la), len(lb)) {
			at := join(path, name) + "[" + strconv.Itoa(i) + "]"
			if err := d.child(elem(la, i), elem(lb, i), at, depth); err != nil {
				return err
			}
		}
	}
	return nil
}

// child diffs one child or list element: equal is no change, one side absent is a
// change, and two present nodes are walked.
func (d *differ) child(a, b Hash, path string, depth int) error {
	switch {
	case a == b:
		return nil
	case a.IsZero() || b.IsZero():
		d.changes = append(d.changes, Change{Path: path, Before: a, After: b})
		return nil
	default:
		return d.node(a, b, path, depth+1)
	}
}

// fields reports each field whose canonical value differs between na and nb.
func (d *differ) fields(na, nb Node, a, b Hash, path string) error {
	for _, name := range unionKeys(na.Fields, nb.Fields) {
		va, err := canonicalValue(na.Fields[name])
		if err != nil {
			return err
		}
		vb, err := canonicalValue(nb.Fields[name])
		if err != nil {
			return err
		}
		if !bytes.Equal(va, vb) {
			d.changes = append(
				d.changes,
				Change{Path: join(path, name), Before: a, After: b},
			)
		}
	}
	return nil
}

// canonicalValue returns the RFC 8785 form of one field value; a nil or typed nil is
// absent.
func canonicalValue(v any) ([]byte, error) {
	if absent(v) {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: encode field")
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"chartspec: canonicalize field",
		)
	}
	return out, nil
}

// unionKeys returns the keys of both maps, sorted.
func unionKeys[V any](a, b map[string]V) []string {
	keys := slices.Collect(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// elem returns hs[i], or the zero hash past the end.
func elem(hs []Hash, i int) Hash {
	if i < len(hs) {
		return hs[i]
	}
	return Hash{}
}
