package bulkhead

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"

// Shed runs fn under bh only when a slot is free. ran reports whether fn ran: false means
// the load was shed and err is bh's rejection; true means err is fn's own. The flag
// tells a shed from fn's error without depending on a backend's full-sentinel, so a
// server can reject with its framework's ResourceExhausted while passing a handler's
// own failure through.
func Shed(bh interfaces.Bulkhead, fn func() error) (ran bool, err error) {
	shedErr := bh.TryExecute(func() error {
		ran = true
		err = fn()
		return err
	})
	if !ran {
		return false, shedErr
	}
	return true, err
}
