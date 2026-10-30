package errcodes

import (
	"bytes"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// The retry classes a row may declare.
const (
	// retryTransient is a failure a retry may fix.
	retryTransient = "transient"

	// retryPermanent is a failure no retry fixes.
	retryPermanent = "permanent"

	// retryNone is neither: the caller decides (a spent quota, a server fault).
	retryNone = "none"
)

// taxonomy is the decoded source file.
type taxonomy struct {
	// Codes are the rows, in output order.
	Codes []code `yaml:"codes"`
}

// code is one taxonomy row (the source file's header documents each field).
type code struct {
	// Code is the wire value.
	Code string `yaml:"code"`

	// Go is the Go constant's name.
	Go string `yaml:"go"`

	// Doc is a noun phrase saying what the code means.
	Doc string `yaml:"doc"`

	// Note is an optional further sentence.
	Note string `yaml:"note"`

	// GRPC is the gRPC status name.
	GRPC string `yaml:"grpc"`

	// Retry is the retry class: transient, permanent or none.
	Retry string `yaml:"retry"`

	// SafeMessage is the fixed client-safe message; empty means the generic one.
	SafeMessage string `yaml:"safe_message"`

	// HTTP is the HTTP status.
	HTTP int `yaml:"http"`

	// GRPCNumber is GRPC's status number, resolved by parse.
	GRPCNumber int `yaml:"-"`

	// HideMessage hides the error's own message at the REST edge.
	HideMessage bool `yaml:"hide_message"`

	// FixedClientMessage sends SafeMessage instead of the error's own message at the
	// Python gRPC edge.
	FixedClientMessage bool `yaml:"fixed_client_message"`
}

// Pattern checks on a row's names.
var (
	// codeRE is a wire value: upper snake case.
	codeRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

	// goRE is a Go constant name.
	goRE = regexp.MustCompile(`^Code[A-Z][A-Za-z0-9]*$`)
)

// grpcNumber returns the number of the gRPC status named name.
func grpcNumber(name string) (int, bool) {
	names := [...]string{
		"OK",
		"CANCELLED",
		"UNKNOWN",
		"INVALID_ARGUMENT",
		"DEADLINE_EXCEEDED",
		"NOT_FOUND",
		"ALREADY_EXISTS",
		"PERMISSION_DENIED",
		"RESOURCE_EXHAUSTED",
		"FAILED_PRECONDITION",
		"ABORTED",
		"OUT_OF_RANGE",
		"UNIMPLEMENTED",
		"INTERNAL",
		"UNAVAILABLE",
		"DATA_LOSS",
		"UNAUTHENTICATED",
	}
	for i, n := range names {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// parse decodes src strictly and validates every row: a unique, well-formed code and Go
// name, a doc, an HTTP status, a known gRPC status and retry class, and a safe message
// wherever the row sends one in place of its own. A violation is CodeInvalidInput.
func parse(src []byte) ([]code, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	var tx taxonomy
	if err := dec.Decode(&tx); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "decode "+SourcePath)
	}
	if len(tx.Codes) == 0 {
		return nil, apperr.InvalidInput(SourcePath + ": no codes")
	}
	seen := map[string]bool{}
	for i := range tx.Codes {
		c := &tx.Codes[i]
		if err := validate(c, seen); err != nil {
			return nil, err
		}
	}
	return tx.Codes, nil
}

// validate checks one row against the rules parse documents, resolves its gRPC number
// and records its names in seen.
func validate(c *code, seen map[string]bool) error {
	bad := func(why string) error {
		return apperr.InvalidInput(
			SourcePath + ": code " + strconv.Quote(c.Code) + ": " + why,
		)
	}
	switch {
	case !codeRE.MatchString(c.Code):
		return bad("the code is not upper snake case")
	case !goRE.MatchString(c.Go):
		return bad("go must be an exported Code<Name> constant")
	case c.Doc == "":
		return bad("doc is empty")
	case c.HTTP < 100 || c.HTTP > 599:
		return bad("http is not a status code")
	case c.Retry != retryTransient && c.Retry != retryPermanent && c.Retry != retryNone:
		return bad("retry must be transient, permanent or none")
	case c.FixedClientMessage && c.SafeMessage == "":
		return bad("fixed_client_message needs a safe_message")
	case seen["code "+c.Code] || seen["go "+c.Go]:
		return bad("the code or its Go name is declared twice")
	}
	n, ok := grpcNumber(c.GRPC)
	if !ok {
		return bad("grpc " + strconv.Quote(c.GRPC) + " is not a gRPC status")
	}
	c.GRPCNumber = n
	seen["code "+c.Code], seen["go "+c.Go] = true, true
	return nil
}
