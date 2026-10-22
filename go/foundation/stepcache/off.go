package stepcache

import "context"

// off is the cache that never hits.
type off struct{}

// Hit reports a miss.
func (off) Hit(context.Context, string) (bool, error) { return false, nil }

// Record does nothing.
func (off) Record(context.Context, string) error { return nil }
