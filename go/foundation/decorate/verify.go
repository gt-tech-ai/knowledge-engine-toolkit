package decorate

import (
	"maps"
	"slices"
	"strings"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
)

// registry records the components one Decorations resolved, by tier, and the
// resolutions that failed. It is written while the composition root builds the graph
// and read by Verify.
type registry struct {
	// used maps a tier name to the component names resolved in it.
	used map[string]map[string]struct{}

	// errs are the resolution errors Subject recorded.
	errs []error

	// mu guards used and errs.
	mu sync.Mutex
}

// newRegistry returns an empty registry.
func newRegistry() *registry {
	return &registry{used: map[string]map[string]struct{}{}}
}

// use records name as resolved in tier; a nil registry records nothing.
func (r *registry) use(tier, name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.used[tier] == nil {
		r.used[tier] = map[string]struct{}{}
	}
	r.used[tier][name] = struct{}{}
}

// fail records a resolution error; a nil registry records nothing.
func (r *registry) fail(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

// covers reports whether the override key in tier applies to a resolved component:
// one named key, or one beneath it. The caller holds mu.
func (r *registry) covers(tier, key string) bool {
	for name := range r.used[tier] {
		if name == key || strings.HasPrefix(name, key+".") {
			return true
		}
	}
	return false
}

// Verify checks the decoration of a built graph; the composition root calls it once
// every component is built. It fails CodeInvalidInput with every resolution error
// Subject recorded, and with every override owned by one of prefixes (named a prefix,
// or beneath one) that applies to no component resolved — a misspelled or stale key
// that would otherwise configure nothing. Several deployables share one config, so
// each judges only the overrides under its own prefixes. The zero Decorations verifies
// nothing.
func (d Decorations) Verify(prefixes ...string) error {
	if d.reg == nil {
		return nil
	}
	d.reg.mu.Lock()
	defer d.reg.mu.Unlock()
	errs := slices.Clone(d.reg.errs)
	if d.cfg != nil {
		var unused []string
		sections := d.cfg.Sections()
		for i := range sections {
			s := &sections[i]
			for _, key := range slices.Sorted(maps.Keys(s.Tier.Overrides)) {
				if owned(key, prefixes) && !d.reg.covers(s.Name, key) {
					unused = append(
						unused,
						decoration.SectionKey+"."+s.Key+".overrides."+key,
					)
				}
			}
		}
		if len(unused) > 0 {
			errs = append(errs, apperr.New(
				apperr.CodeInvalidInput,
				"no component uses "+strings.Join(unused, ", "),
			))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return apperr.Wrap(apperr.Join(errs...), apperr.CodeInvalidInput, "decoration")
}

// owned reports whether key is one of prefixes or beneath one.
func owned(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if key == p || strings.HasPrefix(key, p+".") {
			return true
		}
	}
	return false
}

// check fails CodeInvalidInput when l, the overrides of the component named name,
// turns on a layer t does not order: the layer would be accepted and never run.
func (t Tier) check(name string, l decoration.Layers) error {
	for _, s := range []struct {
		key   string
		layer Layer
		on    bool
	}{
		{"recovery", LayerRecovery, decoration.On(l.Recovery)},
		{"tracing", LayerTracing, decoration.On(l.Tracing)},
		{"metrics", LayerMetrics, decoration.On(l.Metrics)},
		{"logging", LayerLogging, decoration.On(l.Logging)},
		{"auth", LayerAuth, decoration.On(l.Auth)},
		{"timeout", LayerTimeout, l.Timeout != nil && *l.Timeout > 0},
		{"retry", LayerRetry, decoration.On(l.Retry.Enabled)},
		{"circuit_breaker", LayerCircuitBreaker, decoration.On(l.CircuitBreaker.Enabled)},
		{"bulkhead", LayerBulkhead, decoration.On(l.Bulkhead.Enabled)},
		{"rate_limit", LayerRateLimit, decoration.On(l.RateLimit.Enabled)},
	} {
		if s.on && !slices.Contains(t.Order, s.layer) {
			return apperr.New(apperr.CodeInvalidInput, "decoration: "+t.Name+" "+name+
				" turns on "+s.key+", which the "+t.Name+" tier does not run")
		}
	}
	return nil
}
