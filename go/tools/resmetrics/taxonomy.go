package resmetrics

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// The series kinds a row may declare.
const (
	// kindCounter is a monotonic count; its name ends in _total.
	kindCounter = "counter"

	// kindGauge is a point-in-time value; its name does not end in _total.
	kindGauge = "gauge"
)

// taxonomy is the decoded source file.
type taxonomy struct {
	// Enums are the closed label value sets, in output order.
	Enums []enum `yaml:"enums"`

	// Series are the rows, in output order.
	Series []series `yaml:"series"`
}

// enum is one closed set of label values (the source file's header documents each
// field).
type enum struct {
	// Name is the snake_case name a label's enum refers to.
	Name string `yaml:"name"`

	// Go is the Go type name, which the Python StrEnum shares.
	Go string `yaml:"go"`

	// Doc is a sentence saying what the values mean.
	Doc string `yaml:"doc"`

	// Values are the enum's values, in output order.
	Values []enumValue `yaml:"values"`
}

// enumValue is one value of an enum.
type enumValue struct {
	// Gauge is the number a gauge reports for the value; nil when the enum is not a
	// gauge's value.
	Gauge *float64 `yaml:"gauge"`

	// Name is the wire value.
	Name string `yaml:"name"`

	// Doc is a sentence saying what the value means.
	Doc string `yaml:"doc"`
}

// series is one metric row.
type series struct {
	// ValueEnum is the enum Value names, resolved by parse.
	ValueEnum *enum `yaml:"-"`

	// Value names the enum a gauge's method sets, or is empty for a plain float gauge.
	Value string `yaml:"value"`

	// Name is the metric name.
	Name string `yaml:"name"`

	// Kind is counter or gauge.
	Kind string `yaml:"kind"`

	// Method is the Go Recorder method name.
	Method string `yaml:"method"`

	// Help is the metric's help text.
	Help string `yaml:"help"`

	// Labels are the series' own labels, after the bound tier and name.
	Labels []label `yaml:"labels"`
}

// label is one of a series' own labels.
type label struct {
	// Name is the label name.
	Name string `yaml:"name"`

	// Enum names the enum closing the label's values; empty for an open label.
	Enum string `yaml:"enum"`

	// Type is the enum's Go type, resolved by parse; empty for an open label.
	Type string `yaml:"-"`
}

// Pattern checks on a row's names.
var (
	// seriesRE is a series name: resilience_ then snake case.
	seriesRE = regexp.MustCompile(`^resilience_[a-z][a-z0-9_]*$`)

	// methodRE is an exported Go method name.
	methodRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

	// snakeRE is a snake_case name or wire value.
	snakeRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// knownLabel reports whether name is a label a series may carry after the bound tier
// and name.
func knownLabel(name string) bool {
	switch name {
	case "operation", "reason", "outcome", "to":
		return true
	default:
		return false
	}
}

// parse decodes src strictly and validates it: every enum well formed and within
// maxEnumValues, every series named resilience_* with a unique name and method, a
// counter ending in _total and a gauge not, a help text, known labels and enums, and a
// gauge value enum whose values all carry a gauge number. A violation is
// CodeInvalidInput.
func parse(src []byte) (taxonomy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)

	var current taxonomy

	err := dec.Decode(&current)
	if err != nil {
		return taxonomy{}, apperr.Wrap(err, apperr.CodeInvalidInput, "decode "+SourcePath)
	}

	if len(current.Series) == 0 {
		return taxonomy{}, apperr.InvalidInput(SourcePath + ": no series")
	}

	enums, err := indexEnums(current.Enums)
	if err != nil {
		return taxonomy{}, err
	}

	err = checkSeries(current.Series, enums)
	if err != nil {
		return taxonomy{}, err
	}

	return current, nil
}

// indexEnums validates every enum and returns them by name.
func indexEnums(list []enum) (map[string]*enum, error) {
	enums := map[string]*enum{}

	for index := range list {
		why := enumProblem(&list[index], enums)
		if why == "" {
			why = valuesProblem(list[index].Values)
		}

		if why != "" {
			return nil, rowError("enum", list[index].Name, why)
		}

		enums[list[index].Name] = &list[index]
	}

	return enums, nil
}

// checkSeries validates every row against the naming rules and resolves its labels and
// value enum from enums.
func checkSeries(rows []series, enums map[string]*enum) error {
	seen := map[string]bool{}

	for index := range rows {
		row := &rows[index]

		why := rowProblem(row, seen, enums)
		if why != "" {
			return rowError("series", row.Name, why)
		}

		seen["name "+row.Name], seen["method "+row.Method] = true, true
	}

	return nil
}

// rowProblem runs every series rule on row in order and returns the first failure,
// or "" when it passes them all.
func rowProblem(row *series, seen map[string]bool, enums map[string]*enum) string {
	for _, check := range []func() string{
		func() string { return nameProblem(row) },
		func() string { return fieldProblem(row, seen) },
		func() string { return resolveLabels(row, enums) },
		func() string { return resolveValue(row, enums) },
	} {
		if why := check(); why != "" {
			return why
		}
	}

	return ""
}

// rowError names the row a rule rejected.
func rowError(what, name, why string) error {
	return apperr.InvalidInput(
		SourcePath + ": " + what + " " + strconv.Quote(name) + ": " + why,
	)
}

// enumProblem says which rule the enum's own fields break, or "" when none.
func enumProblem(current *enum, enums map[string]*enum) string {
	switch {
	case !snakeRE.MatchString(current.Name):
		return "the name is not snake case"
	case !methodRE.MatchString(current.Go):
		return "go must be an exported type name"
	case current.Doc == "":
		return "doc is empty"
	case len(current.Values) == 0 || len(current.Values) > maxEnumValues:
		return "an enum has 1 to " + strconv.Itoa(maxEnumValues) + " values"
	case enums[current.Name] != nil:
		return "the enum is declared twice"
	default:
		return ""
	}
}

// valuesProblem says which value is malformed or repeated, or that only some values
// carry a gauge number (the templates render every value's number or none), or ""
// when none.
func valuesProblem(values []enumValue) string {
	seen := map[string]bool{}

	for _, value := range values {
		if !snakeRE.MatchString(value.Name) || value.Doc == "" || seen[value.Name] {
			return "value " + strconv.Quote(value.Name) +
				" needs a unique snake_case name and a doc"
		}

		seen[value.Name] = true
	}

	return gaugeProblem(values)
}

// gaugeProblem says that only some of values carry a gauge number, or "" when every
// value or none does.
func gaugeProblem(values []enumValue) string {
	numbered := 0

	for _, value := range values {
		if value.Gauge != nil {
			numbered++
		}
	}

	if numbered != 0 && numbered != len(values) {
		return "gauge numbers must be on every value or none"
	}

	return ""
}

// nameProblem says which rule the row's name and kind break, or "" when none.
func nameProblem(row *series) string {
	total := strings.HasSuffix(row.Name, "_total")

	switch {
	case !seriesRE.MatchString(row.Name):
		return "the name must be resilience_ then snake case"
	case row.Kind != kindCounter && row.Kind != kindGauge:
		return "kind must be counter or gauge"
	case row.Kind == kindCounter && !total:
		return "a counter's name ends in _total"
	case row.Kind == kindGauge && total:
		return "a gauge's name does not end in _total"
	default:
		return ""
	}
}

// fieldProblem says which rule the row's other fields break, or "" when none.
func fieldProblem(row *series, seen map[string]bool) string {
	switch {
	case !methodRE.MatchString(row.Method):
		return "method must be an exported Go name"
	case row.Help == "":
		return "help is empty"
	case seen["name "+row.Name] || seen["method "+row.Method]:
		return "the name or method is declared twice"
	case row.Value != "" && row.Kind != kindGauge:
		return "only a gauge takes a value enum"
	case row.Kind == kindGauge && len(row.Labels) > 0:
		return "a gauge takes no labels (it is set per tier and name)"
	default:
		return ""
	}
}

// resolveLabels checks the row's labels against enums and records each closed label's
// Go type, returning why it fails or "".
func resolveLabels(row *series, enums map[string]*enum) string {
	for index := range row.Labels {
		current := &row.Labels[index]
		if !knownLabel(current.Name) {
			return "label " + strconv.Quote(current.Name) +
				" is not operation, reason, outcome or to"
		}

		if current.Enum == "" {
			continue
		}

		closing, ok := enums[current.Enum]
		if !ok {
			return "label " + current.Name + ": no enum " + strconv.Quote(current.Enum)
		}

		current.Type = closing.Go
	}

	return ""
}

// resolveValue checks a gauge's value enum, whose every value needs a gauge number, and
// records it on the row, returning why it fails or "".
func resolveValue(row *series, enums map[string]*enum) string {
	if row.Value == "" {
		return ""
	}

	valued, ok := enums[row.Value]
	if !ok {
		return "no enum " + strconv.Quote(row.Value)
	}

	for _, value := range valued.Values {
		if value.Gauge == nil {
			return "value enum " + row.Value + ": " + value.Name + " has no gauge number"
		}
	}

	row.ValueEnum = valued

	return ""
}
