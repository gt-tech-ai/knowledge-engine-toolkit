package errcodes

import (
	"bytes"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	var taxonomyValue taxonomy

	err := dec.Decode(&taxonomyValue)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "decode "+SourcePath)
	}

	if len(taxonomyValue.Codes) == 0 {
		return nil, apperr.InvalidInput(SourcePath + ": no codes")
	}

	seen := map[string]bool{}

	for i := range taxonomyValue.Codes {
		c := &taxonomyValue.Codes[i]

		err := validate(c, seen)
		if err != nil {
			return nil, err
		}
	}

	return taxonomyValue.Codes, nil
}

// validate checks one row against the rules parse documents, resolves its gRPC number
// and records its names in seen.
func validate(codeValue *code, seen map[string]bool) error {
	why := formatProblem(codeValue)
	if why == "" {
		why = policyProblem(codeValue, seen)
	}

	if why != "" {
		return rowError(codeValue, why)
	}

	count, ok := grpcNumber(codeValue.GRPC)
	if !ok {
		return rowError(
			codeValue,
			"grpc "+strconv.Quote(codeValue.GRPC)+" is not a gRPC status",
		)
	}

	codeValue.GRPCNumber = count
	seen["code "+codeValue.Code], seen["go "+codeValue.Go] = true, true

	return nil
}

// rowError names the row a rule rejected.
func rowError(c *code, why string) error {
	return apperr.InvalidInput(
		SourcePath + ": code " + strconv.Quote(c.Code) + ": " + why,
	)
}

// formatProblem says which field is malformed, or "" when all are well formed.
func formatProblem(codeValue *code) string {
	switch {
	case !codeRE.MatchString(codeValue.Code):
		return "the code is not upper snake case"
	case !goRE.MatchString(codeValue.Go):
		return "go must be an exported Code<Name> constant"
	case codeValue.Doc == "":
		return "doc is empty"
	case codeValue.HTTP < http100 || codeValue.HTTP > http599:
		return "http is not a status code"
	default:
		return ""
	}
}

// policyProblem says which cross-field rule the row breaks, or "" when none.
func policyProblem(codeValue *code, seen map[string]bool) string {
	switch {
	case codeValue.Retry != retryTransient && codeValue.Retry != retryPermanent &&
		codeValue.Retry != retryNone:
		return "retry must be transient, permanent or none"
	case codeValue.FixedClientMessage && codeValue.SafeMessage == "":
		return "fixed_client_message needs a safe_message"
	case seen["code "+codeValue.Code] || seen["go "+codeValue.Go]:
		return "the code or its Go name is declared twice"
	default:
		return ""
	}
}
