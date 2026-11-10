package types

// Capture stores a step's output in a variable.
type Capture struct {
	// Var is the variable.
	Var string `yaml:"var" json:"var"`

	// As is CaptureText (the default), CaptureLines, CaptureJSON or CaptureExit.
	As string `yaml:"as" json:"as,omitempty"`
}
