package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// compressionThreshold is the minimum buffered response size (bytes) before
// gzip kicks in; payloads below this are sent uncompressed to avoid spending
// CPU on bodies too small to benefit.
const compressionThreshold = 1024 // 1KB

// newGzipWriterPool returns a pool that reuses *gzip.Writer instances across
// requests to avoid per-request allocation; writers are Reset onto the target
// before use and returned on Close.
func newGzipWriterPool() *sync.Pool {
	return &sync.Pool{
		New: func() any {
			w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
			return w
		},
	}
}

// Compression returns middleware that gzip-compresses responses larger than 1KB.
// Each returned middleware owns one gzip writer pool shared by its requests.
func Compression() Middleware {
	pool := newGzipWriterPool()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}

			cw := &compressWriter{
				ResponseWriter: w,
				request:        r,
				pool:           pool,
			}
			defer cw.Close()

			next.ServeHTTP(cw, r)
		})
	}
}

// compressWriter is an http.ResponseWriter that buffers the response and
// transparently switches to gzip once the buffered size crosses
// compressionThreshold; smaller responses are flushed uncompressed on Close.
type compressWriter struct {
	// ResponseWriter is the original writer that receives the final output.
	http.ResponseWriter

	// request is the inbound HTTP request, retained to inspect Accept-Encoding.
	request *http.Request

	// pool supplies and takes back the gzip writer; it belongs to the Compression
	// middleware that created this writer.
	pool *sync.Pool

	// gzWriter is the gzip compressor, initialized once the buffer exceeds the threshold.
	gzWriter *gzip.Writer

	// buf accumulates response bytes until the compression threshold is reached.
	buf []byte

	// headerSent tracks whether Content-Encoding headers have been written.
	headerSent bool
}

// Write buffers bytes until the compression threshold is reached, then streams
// directly through the gzip writer. It always reports the full input length as
// written so callers see no short writes regardless of buffering state.
func (cw *compressWriter) Write(b []byte) (int, error) {
	if cw.gzWriter != nil {
		// Already compressing — write directly to gzip
		n, err := cw.gzWriter.Write(b)
		return n, apperr.Wrap(err, apperr.CodeInternal, "gzip write")
	}

	cw.buf = append(cw.buf, b...)

	if len(cw.buf) >= compressionThreshold {
		cw.startGzip()
		// startGzip wrote the full buffer (which includes b)
		return len(b), nil
	}

	return len(b), nil
}

// startGzip transitions the writer into compressing mode: it sets the
// Content-Encoding header, drops the now-invalid Content-Length, grabs a pooled
// gzip writer, and flushes any already-buffered bytes through it.
func (cw *compressWriter) startGzip() {
	cw.headerSent = true
	cw.Header().Set("Content-Encoding", "gzip")
	cw.Header().Del("Content-Length")

	gz := cw.pool.Get().(*gzip.Writer)
	gz.Reset(cw.ResponseWriter)
	cw.gzWriter = gz

	// Write buffered content
	if len(cw.buf) > 0 {
		_, _ = cw.gzWriter.Write(cw.buf)
		cw.buf = nil
	}
}

// Close finalizes the response: it flushes and returns the gzip writer to the
// pool when compression was engaged, or writes the still-buffered (sub-threshold)
// bytes uncompressed otherwise. It is invoked via defer by the Compression
// middleware after the handler returns.
func (cw *compressWriter) Close() {
	if cw.gzWriter != nil {
		_ = cw.gzWriter.Close()
		cw.pool.Put(cw.gzWriter)
		return
	}
	if len(cw.buf) > 0 {
		// Below threshold, write uncompressed
		_, _ = cw.ResponseWriter.Write(cw.buf)
	}
}
