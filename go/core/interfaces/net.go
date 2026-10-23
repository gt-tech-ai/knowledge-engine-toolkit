package interfaces

import (
	"context"
	"net"
	"net/http"
)

// HTTPDoer sends one HTTP request, with *http.Client's own signature so a client
// satisfies it unchanged.
type HTTPDoer interface {
	// Do sends req and returns its response; the caller closes the body.
	Do(req *http.Request) (*http.Response, error)
}

// NSResolver looks up a host's name servers, with *net.Resolver's own signature so a
// resolver satisfies it unchanged.
type NSResolver interface {
	// LookupNS returns the authoritative name servers a public resolver sees for host.
	LookupNS(ctx context.Context, host string) ([]*net.NS, error)
}
