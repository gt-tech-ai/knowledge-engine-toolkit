package decorate

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"

// Observer is what a resilience layer reports through (series.Observer): the
// resilience_* recorder bound to the layer's tier and subject, and the logger its Warn
// lines go to. The zero value reports nothing, so a layer built without metrics or
// logging behaves as before.
type Observer = series.Observer

// newObserver builds the Observer for the subject named name in tier from deps: the
// recorder follows the metrics layer (Deps.Metrics is set only when it is on) and the
// logger follows the logging layer.
func newObserver(tier, name string, deps *Deps) Observer {
	return series.NewObserver(deps.Metrics, deps.Logger, tier, name)
}
