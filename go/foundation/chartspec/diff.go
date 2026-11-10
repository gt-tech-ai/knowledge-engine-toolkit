package chartspec

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	"github.com/gowebpki/jcs"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
func Diff(digest, hash Hash, get NodeGetter) ([]Change, error) {
	if digest == hash {
		return nil, nil
	}

	if digest.IsZero() || hash.IsZero() {
		return []Change{{Before: digest, After: hash, Path: ""}}, nil
	}

	currentDiffer := differ{dec: decoder{get: get}, changes: nil}

	err := currentDiffer.node(digest, hash, "", 0)
	if err != nil {
		return nil, err
	}

	return currentDiffer.changes, nil
}

// join appends a member name to a path.
func join(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}

// node diffs two differing, non-zero nodes at path.
func (d *differ) node(hash, digest Hash, path string, depth int) error {
	if depth > maxTreeDepth {
		return apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: tree nesting exceeds max depth",
		)
	}

	currentNode, err := d.dec.fetch(hash)
	if err != nil {
		return err
	}

	secondNode, err := d.dec.fetch(digest)
	if err != nil {
		return err
	}

	if currentNode.Kind != secondNode.Kind {
		d.changes = append(d.changes, Change{Path: path, Before: hash, After: digest})

		return nil
	}

	err = d.fields(currentNode, secondNode, hash, digest, path)
	if err != nil {
		return err
	}

	err = d.children(&currentNode, &secondNode, path, depth)
	if err != nil {
		return err
	}

	return d.lists(&currentNode, &secondNode, path, depth)
}

// children diffs the named children of two nodes.
func (d *differ) children(na, nb *Node, path string, depth int) error {
	for _, name := range unionKeys(na.Children, nb.Children) {
		err := d.child(na.Children[name], nb.Children[name], join(path, name), depth)
		if err != nil {
			return err
		}
	}

	return nil
}

// lists diffs the named lists of two nodes, element by element.
func (d *differ) lists(na, nb *Node, path string, depth int) error {
	for _, name := range unionKeys(na.Lists, nb.Lists) {
		err := d.list(na.Lists[name], nb.Lists[name], join(path, name), depth)
		if err != nil {
			return err
		}
	}

	return nil
}

// list diffs two hash lists by position, a missing element reading as absent.
func (d *differ) list(la, lb []Hash, path string, depth int) error {
	for i := range max(len(la), len(lb)) {
		err := d.child(elem(la, i), elem(lb, i), path+"["+strconv.Itoa(i)+"]", depth)
		if err != nil {
			return err
		}
	}

	return nil
}

// child diffs one child or list element: equal is no change, one side absent is a
// change, and two present nodes are walked.
func (d *differ) child(digest, hash Hash, path string, depth int) error {
	switch {
	case digest == hash:
		return nil
	case digest.IsZero() || hash.IsZero():
		d.changes = append(d.changes, Change{Path: path, Before: digest, After: hash})

		return nil
	default:
		return d.node(digest, hash, path, depth+1)
	}
}

// fields reports each field whose canonical value differs between na and nb.
func (d *differ) fields(na, node Node, hash, digest Hash, path string) error {
	for _, name := range unionKeys(na.Fields, node.Fields) {
		data, err := canonicalValue(na.Fields[name])
		if err != nil {
			return err
		}

		vb, err := canonicalValue(node.Fields[name])
		if err != nil {
			return err
		}

		if !bytes.Equal(data, vb) {
			d.changes = append(
				d.changes,
				Change{Path: join(path, name), Before: hash, After: digest},
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
