// Package opmockgen holds the contracts opmockgen's tests generate mocks for, and the
// committed mocks it generates (mock_ops_gen.go): a struct of operations beside an
// engine role, with an embedded struct of further operations.
package opmockgen

//go:generate go run github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/opmockgen/cmd/opmockgen -out mock_ops_gen.go Notes

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"

// Lookups are the notes' reads beyond the role.
type Lookups struct {
	// Find returns the id of the note titled by its input.
	Find interfaces.Op[string, int]
}

// Notes is a contract: an engine role, an embedded struct of operations, and an
// operation of its own.
type Notes struct {
	// Reader reads a note by id.
	interfaces.Reader[string, int]

	// Lookups are the reads beyond the role.
	Lookups

	// Count returns the number of notes.
	Count interfaces.Op[struct{}, int]
}

// Tagger is a contract-local interface: its mock is the mockgen mock of the same name in
// the generated file's package.
type Tagger interface {
	// Tags lists a note's tags.
	Tags(id int) []string
}

// Tagged is a contract with a local interface field.
type Tagged struct {
	// Tagger lists tags.
	Tagger Tagger
}

// Clash promotes two fields to the name Find, which a flattened mock cannot hold.
type Clash struct {
	// Lookups holds one Find.
	Lookups

	// Find is the other.
	Find interfaces.Op[string, int]
}
