// Package ollama provides a testcontainers-based Ollama server with one small chat model
// pulled, for integration tests. It mirrors the elasticmq/redis siblings: start a
// container, wait for its API port, and expose the base URL so the caller builds its own
// client. No LLM client dependency lives here.
package ollama

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

const (
	// ollamaImage is the Ollama container image the fixture starts.
	ollamaImage = "ollama/ollama:0.35.0"

	// ollamaPort is the Ollama HTTP API port exposed by the container.
	ollamaPort = "11434/tcp"

	// Model is the chat model the fixture pulls: the smallest of the qwen2.5 family.
	Model = "qwen2.5:0.5b"

	// pullTimeout bounds the model pull (a few hundred MB on first run).
	pullTimeout = 10 * time.Minute
)

// TestOllama wraps a testcontainers Ollama instance with Model pulled.
type TestOllama struct {
	// container is the running Ollama testcontainer (nil after Close).
	container testcontainers.Container

	// baseURL is the API root (http://host:port).
	baseURL string
}

// NewTestOllama starts an Ollama container, waits up to 60 seconds for its API port and
// pulls Model, bounded by a 10-minute deadline.
func NewTestOllama(ctx context.Context) (*TestOllama, error) {
	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        ollamaImage,
				ExposedPorts: []string{ollamaPort},
				WaitingFor: wait.ForListeningPort(ollamaPort).
					WithStartupTimeout(60 * time.Second),
			},
			Started: true,
		},
	)
	if err != nil {
		// A failed start can still leave a container behind; TerminateContainer is
		// nil-safe.
		_ = testcontainers.TerminateContainer(container)
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to start Ollama container",
		)
	}
	o := &TestOllama{container: container}
	if err := o.pull(ctx); err != nil {
		o.Close(ctx)
		return nil, err
	}
	host, err := container.Host(ctx)
	if err != nil {
		o.Close(ctx)
		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get container host")
	}
	port, err := container.MappedPort(ctx, ollamaPort)
	if err != nil {
		o.Close(ctx)
		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}
	o.baseURL = fmt.Sprintf("http://%s:%s", host, port.Port())
	return o, nil
}

// pull runs `ollama pull Model` inside the container; a non-zero exit is an error
// quoting its output.
func (o *TestOllama) pull(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	code, out, err := o.container.Exec(ctx, []string{"ollama", "pull", Model})
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "failed to pull "+Model)
	}
	if code != 0 {
		msg, _ := io.ReadAll(out)
		return apperr.New(apperr.CodeInternal,
			fmt.Sprintf("ollama pull %s exited %d: %s", Model, code, msg))
	}
	return nil
}

// BaseURL returns the API root (http://host:port).
func (o *TestOllama) BaseURL() string { return o.baseURL }

// Close terminates the Ollama container. Safe to call on a nil receiver.
func (o *TestOllama) Close(ctx context.Context) {
	if o != nil && o.container != nil {
		o.container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on teardown
	}
}
