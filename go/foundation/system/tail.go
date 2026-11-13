package system

import (
	"bytes"
	"io"
)

// stderrTailBytes bounds the stderr a streamed command's failure carries: enough for
// the last screenful of a compiler or compose error, small enough for a log field.
const stderrTailBytes = 4096

// Compile-time interface compliance check.
var _ io.Writer = (*tailBuffer)(nil)

// tailBuffer keeps the last stderrTailBytes written to it. A streamed command's
// stderr is teed into one so its failure can say why without buffering the whole
// stream.
type tailBuffer struct {
	// buf holds at most stderrTailBytes of the most recent output.
	buf []byte

	// cut records that earlier output was dropped, so the first line may be partial.
	cut bool
}

// Write appends chunk, dropping the oldest bytes past the bound. It never fails, so the
// tee never stops the stream it copies.
func (t *tailBuffer) Write(chunk []byte) (int, error) {
	t.buf = append(t.buf, chunk...)
	if over := len(t.buf) - stderrTailBytes; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.cut = true
	}

	return len(chunk), nil
}

// String is the kept output, trimmed; when output was dropped, the partial first
// line is dropped too so the tail starts on a whole line.
func (t *tailBuffer) String() string {
	kept := t.buf
	if t.cut {
		if i := bytes.IndexByte(kept, '\n'); i >= 0 {
			kept = kept[i+1:]
		}
	}

	return string(bytes.TrimSpace(kept))
}
