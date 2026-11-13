// Package github is the ConnectorSource over one branch of a GitHub repository, read as
// a GitHub App installation: the branch head's recursive git tree, one object per file.
// Errors are coded for the client tier: a transport failure, a 5xx, a 429 or a rate
// limit is CodeUnavailable (retried); a missing repository, branch or installation and
// any other 4xx are CodeInvalidInput.
package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/httperr"
)

// DefaultBaseURL is the GitHub REST API root; GitHub Enterprise Server sets its own.
const DefaultBaseURL = "https://api.github.com"

// apiVersion is the REST API version every request pins.
const apiVersion = "2022-11-28"

// requestTimeout bounds one API request, whatever deadline the caller's context has.
const requestTimeout = 30 * time.Second

// maxErrorBody caps how much of a failed response's body an error quotes.
const maxErrorBody = 512

// api sends REST requests to one GitHub API root.
type api struct {
	// doer sends each request.
	doer interfaces.HTTPDoer

	// base is the API root, without a trailing slash.
	base string
}

// newAPI returns the api for baseURL (empty: DefaultBaseURL) sending through doer.
func newAPI(doer interfaces.HTTPDoer, baseURL string) api {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	return api{doer: doer, base: strings.TrimRight(baseURL, "/")}
}

// call sends method to path with authorization (a full Authorization header value)
// and decodes a want-status JSON reply into out; any other status is a coded error.
func (a api) call(
	ctx context.Context,
	method, path, authorization string,
	want int,
	out any,
) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, a.base+path, http.NoBody)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "github: build request")
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", apiVersion)
	req.Header.Set("Authorization", authorization)

	resp, err := a.doer.Do(req)
	if err != nil {
		return httperr.Transport(err, "github: "+method+" "+path)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != want {
		return statusError(resp, method+" "+path)
	}

	err = json.NewDecoder(resp.Body).Decode(out)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "github: decode "+path)
	}

	return nil
}

// statusError codes an unexpected reply to op: a 5xx, a 429 and a 403 that reports a
// rate limit are CodeUnavailable, anything else CodeInvalidInput. The message quotes
// the start of the body.
func statusError(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	body := strings.TrimSpace(string(snippet))
	msg := "github: " + op + ": status " + strconv.Itoa(resp.StatusCode) + ": " + body

	if resp.StatusCode >= http.StatusInternalServerError ||
		resp.StatusCode == http.StatusTooManyRequests ||
		rateLimited(resp, body) {
		return apperr.New(apperr.CodeUnavailable, msg)
	}

	return apperr.New(apperr.CodeInvalidInput, msg)
}

// rateLimited reports whether a 403 is GitHub's primary or secondary rate limit
// rather than a refused permission.
func rateLimited(resp *http.Response, body string) bool {
	if resp.StatusCode != http.StatusForbidden {
		return false
	}

	return resp.Header.Get("Retry-After") != "" ||
		resp.Header.Get("X-Ratelimit-Remaining") == "0" ||
		strings.Contains(strings.ToLower(body), "rate limit")
}
