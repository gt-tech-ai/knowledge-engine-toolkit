// Package stub is the KubectlClient with no cluster: reads return empty lists and
// writes succeed without effect.
package stub

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Client is the stub KubectlClient.
type Client struct{}

// Compile-time assertion that Client satisfies KubectlClient.
var _ interfaces.KubectlClient = Client{}

// Get returns an empty list.
func (Client) Get(context.Context, string, string, string) ([]byte, error) {
	return []byte(`{"items":[]}`), nil
}

// Apply does nothing; no files is CodeInvalidInput, as the contract says.
func (Client) Apply(_ context.Context, _ string, files ...string) error {
	if len(files) == 0 {
		return apperr.New(apperr.CodeInvalidInput, "kubectl apply: no files")
	}
	return nil
}

// Rollout reports a finished rollout.
func (Client) Rollout(context.Context, string, string, string, time.Duration) error {
	return nil
}

// CurrentContext returns "stub".
func (Client) CurrentContext(context.Context) (string, error) { return "stub", nil }
