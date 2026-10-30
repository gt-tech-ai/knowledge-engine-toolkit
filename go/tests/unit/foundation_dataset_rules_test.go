package unit_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/dataset"
)

// rulesData is a two-file dataset: a ci section with a job list and a limit, and a
// procedures list whose second row has no steps.
func rulesData(t *testing.T) *dataset.Dataset {
	t.Helper()
	d, err := dataset.Load(fstest.MapFS{
		"data/ci.yaml": {Data: []byte(
			"ci:\n  must_succeed: [build, test, build]\n  max_chars: 70000\n",
		)},
		"data/procs.yaml": {Data: []byte(
			"procedures:\n  - {name: up, steps: [a]}\n  - {name: down, steps: []}\n",
		)},
	}, "data")
	require.NoError(t, err)
	return d
}

// TestFoundationDataset_Check tests the CEL rules a front-end runs over its merged data.
//
// Why this test is important:
//   - A front-end's data constraints (a non-empty list, distinct ids, a bounded limit,
//     every row complete) belong beside the data as rows, not in hand-written Validate
//     methods; a rule that silently passes, or a failure that does not say which rule
//     and which row, would let malformed data load.
//
// What it tests:
//   - Each top-level section is a variable, and data is the whole set.
//   - A rule evaluating true passes; false fails with its message, prefixed by its name.
//   - A rule evaluating to a list of strings fails once per string; an empty list passes.
//   - Every failing rule is reported in one INVALID_INPUT error.
//   - A rule that does not compile, or evaluates to neither a bool nor a list of
//     strings, is INVALID_INPUT naming the rule.
//   - No rules, or all passing, is nil.
func TestFoundationDataset_Check(t *testing.T) {
	t.Parallel()
	d := rulesData(t)

	require.NoError(t, d.Check(nil))
	require.NoError(t, d.Check([]dataset.Rule{
		{Name: "listed", Expr: "size(ci.must_succeed) > 0", Message: "list the jobs"},
		{Name: "whole", Expr: "data.ci.max_chars > 0", Message: "positive"},
		{Name: "none", Expr: "procedures.filter(p, p.name == 'x').map(p, p.name)"},
	}))

	err := d.Check([]dataset.Rule{
		{
			Name:    "distinct",
			Expr:    "ci.must_succeed.distinct().size() == ci.must_succeed.size()",
			Message: "ci.must_succeed has a duplicate job id",
		},
		{
			Name:    "bounded",
			Expr:    "ci.max_chars <= 65536",
			Message: "ci.max_chars is over 65536",
		},
		{
			Name: "steps",
			Expr: "procedures.filter(p, size(p.steps) == 0)" +
				".map(p, 'procedure ' + p.name + ' has no steps')",
		},
		{Name: "ok", Expr: "true", Message: "never"},
	})
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "distinct: ci.must_succeed has a duplicate job id")
	assert.Contains(t, err.Error(), "bounded: ci.max_chars is over 65536")
	assert.Contains(t, err.Error(), "steps: procedure down has no steps")
	assert.NotContains(t, err.Error(), "never")

	for name, expr := range map[string]string{
		"bad syntax":   "ci.must_succeed.(",
		"unknown name": "nope.x == 1",
		"wrong type":   "'a string'",
		"mixed list":   "[1, 'a']",
	} {
		err := d.Check([]dataset.Rule{{Name: name, Expr: expr}})
		require.Errorf(t, err, "%s must fail", name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
		assert.Contains(t, err.Error(), name)
	}
}

// TestFoundationDataset_Validate tests the merged data against a JSON Schema.
//
// Why this test is important:
//   - Field-level constraints (an enum, a required key, a minimum) are declared once on
//     the front-end's types and reflected into a schema; the data must be held to that
//     schema at load, or the constraint is documentation only.
//
// What it tests:
//   - Data matching the schema is nil.
//   - Data breaking it is INVALID_INPUT naming the failing location.
//   - A schema that does not compile is INVALID_INPUT.
func TestFoundationDataset_Validate(t *testing.T) {
	t.Parallel()
	d := rulesData(t)

	ok := []byte(`{"type": "object", "properties": {"ci": {"type": "object",
		"properties": {"max_chars": {"type": "integer", "minimum": 1}}}}}`)
	require.NoError(t, d.Validate(ok))

	bad := []byte(`{"type": "object", "properties": {"ci": {"type": "object",
		"properties": {"max_chars": {"type": "integer", "maximum": 65536}}}}}`)
	err := d.Validate(bad)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "/ci/max_chars")

	err = d.Validate([]byte(`{"type": 12}`))
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestFoundationDataset_FromValue tests a dataset built from a Go value.
//
// Why this test is important:
//   - A front-end that builds or edits its data in code (a test overlay, a typed value)
//     must hold it to the same rules as data loaded from files, without rendering it to
//     text and parsing it back.
//
// What it tests:
//   - FromValue yields the value's top-level fields as sections, by their yaml names,
//     decodable and checkable like Load's.
//   - A value that does not encode to a mapping is INVALID_INPUT naming it.
func TestFoundationDataset_FromValue(t *testing.T) {
	t.Parallel()
	type ci struct {
		MustSucceed []string `yaml:"must_succeed"`
	}
	d, err := dataset.FromValue("inline", struct {
		CI ci `yaml:"ci"`
	}{CI: ci{MustSucceed: []string{"a", "a"}}})
	require.NoError(t, err)
	jobs, err := d.Section[map[string][]string]("ci")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "a"}, jobs["must_succeed"])
	err = d.Check([]dataset.Rule{{
		Name: "distinct", Expr: "ci.must_succeed.distinct() == ci.must_succeed",
		Message: "duplicate job",
	}})
	require.ErrorContains(t, err, "distinct: duplicate job")

	_, err = dataset.FromValue("scalar", 3)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "scalar")
}
