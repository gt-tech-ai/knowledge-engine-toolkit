package suite

import (
	"context"

	ollamadb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/ollama"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite/base"
)

// OllamaIntegrationSuite provides a test suite with a real Ollama server and
// ollamadb.Model pulled. OllamaBaseURL is the API root callers build a client on.
type OllamaIntegrationSuite struct {
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite

	// ollama is the running Ollama container, terminated in TearDownSuite.
	ollama *ollamadb.TestOllama
	// OllamaBaseURL is the API root (http://host:port).
	OllamaBaseURL string
}

// SetupSuite starts an Ollama container and pulls the fixture's model.
func (s *OllamaIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	o, err := ollamadb.NewTestOllama(context.Background())
	s.Require().NoError(err, "failed to create test Ollama")

	s.ollama = o
	s.OllamaBaseURL = o.BaseURL()
}

// TearDownSuite terminates the Ollama container.
func (s *OllamaIntegrationSuite) TearDownSuite() {
	if s.ollama != nil {
		s.ollama.Close(context.Background())
	}
}
