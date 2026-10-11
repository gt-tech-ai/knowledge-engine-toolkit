package prometheus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// DecorateDoer runs every request inner sends through the client stack, as the operation
// "prometheus.<last path segment>" (prometheus.query, prometheus.query_range).
//
// Each attempt reads the whole response body before the stack's per-attempt timeout ends,
// so the returned body is buffered and the deadline cannot cut a read short. A body
// larger than maxBodyBytes is CodeInvalidInput (narrow the query); zero or less means no
// limit.
//
// Query requests are GETs, so every attempt is retryable. A transport error keeps a code
// it already carries; otherwise the caller's deadline is CodeTimeout, its cancellation
// CodeCanceled, and any other failure CodeUnavailable. A throttling or unavailable status
// (429, 502, 503, 504) is a CodeUnavailable error, so the stack retries it and its
// breaker counts it. Any other status passes through for the client to code.
func DecorateDoer(
	inner HTTPDoer,
	stack *clientdecorators.Stack,
	maxBodyBytes int64,
) HTTPDoer {
	return &decoratedDoer{inner: inner, stack: stack, maxBodyBytes: maxBodyBytes}
}

// decoratedDoer is the HTTPDoer that sends through the client stack.
type decoratedDoer struct {
	// inner sends each attempt.
	inner HTTPDoer

	// stack applies the client-boundary layers.
	stack *clientdecorators.Stack

	// maxBodyBytes caps a buffered response body; zero or less means no cap.
	maxBodyBytes int64
}

// Do sends req through the stack and returns its response with a buffered body.
func (d *decoratedDoer) Do(req *http.Request) (*http.Response, error) {
	op := "prometheus." + path.Base(req.URL.Path)
	return clientdecorators.Run(req.Context(), d.stack, op,
		clientdecorators.RunOpts{Retryable: true},
		func(ctx context.Context) (*http.Response, error) {
			resp, err := d.inner.Do(req.Clone(ctx))
			if err != nil {
				return nil, transportError(err, "prometheus request")
			}
			body, err := d.readBody(resp.Body)
			if err != nil {
				return nil, err
			}
			if isTransientStatus(resp.StatusCode) {
				return nil, statusError(resp.StatusCode, bytes.NewReader(body))
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		})
}

// readBody reads and closes src, enforcing maxBodyBytes.
func (d *decoratedDoer) readBody(src io.ReadCloser) ([]byte, error) {
	defer func() { _ = src.Close() }()
	r := io.Reader(src)
	if d.maxBodyBytes > 0 {
		r = io.LimitReader(src, d.maxBodyBytes+1)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, transportError(err, "read prometheus response")
	}
	if d.maxBodyBytes > 0 && int64(len(body)) > d.maxBodyBytes {
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"prometheus response exceeds %d bytes; narrow the query", d.maxBodyBytes,
		))
	}
	return body, nil
}
