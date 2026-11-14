package series

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"

// subjectFields is how many key-value entries a Warn line's subject takes at most:
// "tier", the tier, "name", the name.
const subjectFields = 4

// Observer is what a resilience component reports through: the Recorder bound to its
// tier and subject, the logger its Warn lines go to, and the tier and subject those
// lines name (the Recorder is nil when metrics are off, so the lines cannot read them
// from it). The zero value, and a nil *Observer, report nothing.
type Observer struct {
	// Recorder records the component's resilience events; nil records nothing.
	Recorder *Recorder

	// Logger receives the component's Warn lines; nil logs nothing.
	Logger interfaces.Logger

	// Tier is the decoration tier a Warn line names; empty for a standalone primitive,
	// whose lines then name only the subject.
	Tier string

	// Name is the subject a Warn line names.
	Name string
}

// NewObserver returns the Observer for the subject named name in tier: a Recorder over
// metrics (nil when metrics is nil) and logger.
func NewObserver(
	metrics interfaces.Metrics,
	logger interfaces.Logger,
	tier, name string,
) Observer {
	return Observer{
		Recorder: NewRecorder(metrics, tier, name),
		Logger:   logger,
		Tier:     tier,
		Name:     name,
	}
}

// Record returns the Observer's Recorder, nil for a nil Observer; every Recorder
// method is nil-safe, so o.Record().X(…) is always safe.
func (o *Observer) Record() *Recorder {
	if o == nil {
		return nil
	}

	return o.Recorder
}

// Warn writes one Warn line naming the tier (when set) and the subject, then
// keysAndValues; it does nothing for a nil Observer or one without a Logger.
func (o *Observer) Warn(msg string, keysAndValues ...any) {
	if o == nil || o.Logger == nil {
		return
	}

	fields := make([]any, 0, subjectFields+len(keysAndValues))
	if o.Tier != "" {
		fields = append(fields, "tier", o.Tier)
	}

	fields = append(fields, "name", o.Name)
	o.Logger.Warn(msg, append(fields, keysAndValues...)...)
}
