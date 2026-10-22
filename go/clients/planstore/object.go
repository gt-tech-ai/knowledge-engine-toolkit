package planstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// objectStore keeps each unit's plan in an object store and hands the applying job
// only a manifest of object keys and SHA-256 digests; Load refuses a plan whose digest
// differs from the manifest's.
type objectStore struct {
	// storage is the object store.
	storage interfaces.StorageClient

	// log records each plan kept and restored; nil logs nothing.
	log interfaces.Logger
}

// manifest is the hand-off Save writes and Load reads.
type manifest struct {
	// Owner is whose plans these are (PlanSet.Owner).
	Owner string `json:"owner"`

	// Units lists each unit's kept plan, in unit order.
	Units []manifestUnit `json:"units"`
}

// manifestUnit is one unit's kept plan.
type manifestUnit struct {
	// Unit is the unit's path under the plan directory.
	Unit string `json:"unit"`

	// Key is the plan's object key.
	Key string `json:"key"`

	// SHA256 is the hex SHA-256 of the plan as it was kept.
	SHA256 string `json:"sha256"`
}

// newObject builds the object store; it needs a storage client.
func newObject(_ context.Context, _ Config, deps Deps) (interfaces.PlanStore, error) {
	if deps.Storage == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"planstore object: no storage client",
		)
	}
	return objectStore{storage: deps.Storage, log: deps.Log}, nil
}

// Save uploads every unit's plan to <Prefix><RunID>/<unit>/<File> and writes the
// manifest listing each key and SHA-256 into Dir (0o600). An upload failure is
// CodeUpstream naming the unit and object.
func (st objectStore) Save(ctx context.Context, set types.PlanSet) error {
	files, err := FindPlans(set.Dir, set.File)
	if err != nil {
		return err
	}
	m := manifest{Owner: set.Owner}
	for _, f := range files {
		rel, err := filepath.Rel(set.Dir, filepath.Dir(f))
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "locate the unit of "+f)
		}
		unit := filepath.ToSlash(rel)
		data, err := os.ReadFile(f)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "read "+f)
		}
		sum := sha256.Sum256(data)
		key := set.Prefix + set.RunID + "/" + unit + "/" + set.File
		if err := st.storage.Upload(ctx, set.Bucket, key, bytes.NewReader(data),
			"application/octet-stream"); err != nil {
			return apperr.Wrap(err, apperr.CodeUpstream, fmt.Sprintf(
				"store the saved plan of %s in %s/%s", unit, set.Bucket, key))
		}
		st.logf("saved plan stored", "unit", unit, "bucket", set.Bucket, "key", key)
		m.Units = append(m.Units, manifestUnit{
			Unit: unit, Key: key, SHA256: hex.EncodeToString(sum[:]),
		})
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "encode the plan manifest")
	}
	path := filepath.Join(set.Dir, set.Manifest)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "write "+path)
	}
	return nil
}

// Load reads the manifest in Dir and downloads each listed plan to <Dir>/<unit>/<File>.
// It refuses (CodeInvalidInput) a missing or unreadable manifest, one made for another
// owner, one listing no plan, an entry outside the set's plans, and a Dir that already
// holds a plan; an expired (missing) object is CodeNotFound; a digest that differs
// from the manifest's is CodeConflict naming every such unit, whose download is
// removed, so nothing is applied.
func (st objectStore) Load(ctx context.Context, set types.PlanSet) error {
	m, err := readManifest(set)
	if err != nil {
		return err
	}
	present, err := FindPlans(set.Dir, set.File)
	if err != nil {
		return err
	}
	if len(present) > 0 {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"%s already holds saved plans (%s): Load restores only the plans %s lists, "+
				"into a directory that holds none", set.Dir, strings.Join(present, ", "),
			set.Manifest))
	}
	for _, u := range m.Units {
		if err := checkEntry(set, u); err != nil {
			return err
		}
	}
	var mismatched []string
	for _, u := range m.Units {
		dest := filepath.Join(set.Dir, filepath.FromSlash(u.Unit), set.File)
		sum, err := st.fetch(ctx, set.Bucket, u.Key, dest)
		if err != nil {
			return err
		}
		if sum != u.SHA256 {
			_ = os.Remove(dest)
			mismatched = append(mismatched, u.Unit)
			continue
		}
		st.logf("saved plan fetched", "unit", u.Unit, "bucket", set.Bucket, "key", u.Key)
	}
	if len(mismatched) > 0 {
		return apperr.New(apperr.CodeConflict, fmt.Sprintf(
			"the stored plan of %s does not match the SHA-256 in %s, so nothing was "+
				"applied: plan again", strings.Join(mismatched, ", "), set.Manifest))
	}
	return nil
}

// readManifest reads and checks set's manifest: it must exist, parse, name set.Owner
// and list at least one unit.
func readManifest(set types.PlanSet) (manifest, error) {
	path := filepath.Join(set.Dir, set.Manifest)
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, apperr.Wrap(err, apperr.CodeInvalidInput, "read "+path+
			": Load restores the plans a Save listed there")
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+path)
	}
	switch {
	case m.Owner != set.Owner:
		return manifest{}, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"%s lists plans for %q, not %q", path, m.Owner, set.Owner))
	case len(m.Units) == 0:
		return manifest{}, apperr.New(apperr.CodeInvalidInput, path+" lists no plan")
	}
	return m, nil
}

// checkEntry refuses (CodeInvalidInput) an entry whose unit is not a local relative
// path or whose key is not <Prefix>…/<unit>/<File>, so a manifest can neither write
// outside Dir nor fetch another owner's object.
func checkEntry(set types.PlanSet, u manifestUnit) error {
	if !filepath.IsLocal(filepath.FromSlash(u.Unit)) ||
		!strings.HasPrefix(u.Key, set.Prefix) ||
		!strings.HasSuffix(u.Key, "/"+u.Unit+"/"+set.File) {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"the plan manifest entry %q (key %q) is not a plan of %s under %s",
			u.Unit, u.Key, set.Owner, set.Prefix))
	}
	return nil
}

// fetch downloads key to dest (its directory 0o700, the file 0o600, created
// exclusively: a plan holds state and credentials) and returns the hex SHA-256 of what
// it wrote. A missing object is CodeNotFound: kept plans expire under the bucket's
// lifecycle rule, so the applying job waited too long and must plan again; any other
// download failure is CodeUpstream.
func (st objectStore) fetch(
	ctx context.Context,
	bucket, key, dest string,
) (string, error) {
	body, err := st.storage.Download(ctx, bucket, key)
	switch {
	case apperr.Is(err, apperr.CodeNotFound):
		return "", apperr.Wrap(err, apperr.CodeNotFound, fmt.Sprintf(
			"the saved plan %s/%s is gone: kept plans expire under the bucket's "+
				"lifecycle rule; plan again and apply the new plan", bucket, key))
	case err != nil:
		return "", apperr.Wrap(err, apperr.CodeUpstream,
			fmt.Sprintf("fetch the saved plan %s/%s", bucket, key))
	}
	defer func() { _ = body.Close() }()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "create "+filepath.Dir(dest))
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "create "+dest)
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(f, h), body)
	if err := f.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return "", apperr.Wrap(cerr, apperr.CodeUpstream,
			fmt.Sprintf("download the saved plan %s/%s", bucket, key))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// logf records msg when the store has a logger.
func (st objectStore) logf(msg string, kv ...any) {
	if st.log != nil {
		st.log.Info(msg, kv...)
	}
}
