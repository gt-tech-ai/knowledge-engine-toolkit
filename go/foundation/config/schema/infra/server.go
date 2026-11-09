package infra

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// defaultServerConfigReadTimeout is the default ReadTimeout of ServerConfig.
const defaultServerConfigReadTimeout = 30 * time.Second

// defaultServerConfigReadHeaderTimeout is the default ReadHeaderTimeout of ServerConfig.
const defaultServerConfigReadHeaderTimeout = 10 * time.Second

// defaultServerConfigWriteTimeout is the default WriteTimeout of ServerConfig.
const defaultServerConfigWriteTimeout = 30 * time.Second

// defaultServerConfigIdleTimeout is the default IdleTimeout of ServerConfig.
const defaultServerConfigIdleTimeout = 120 * time.Second

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	// Host is the network interface address to bind (e.g. "0.0.0.0" for all interfaces).
	Host string `mapstructure:"host"`

	// Port is the HTTP server listen port (must be overridden per-service; 0 is invalid).
	Port int `mapstructure:"port"`

	// WSPort is the WebSocket server listen port (0 means WebSocket is not enabled).
	WSPort int `mapstructure:"ws_port"`

	// ReadTimeout is the maximum duration to read an entire request including body.
	ReadTimeout time.Duration `mapstructure:"read_timeout"`

	// ReadHeaderTimeout is the maximum duration to read request headers.
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`

	// WriteTimeout is the maximum duration to write a response.
	WriteTimeout time.Duration `mapstructure:"write_timeout"`

	// IdleTimeout is the maximum duration to wait for the next request on a keep-alive
	// connection.
	IdleTimeout time.Duration `mapstructure:"idle_timeout"`
}

// DefaultServerConfig returns a ServerConfig with defaults.
// Port defaults to 0 (must be overridden per-service).
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Host:              "0.0.0.0",
		Port:              0,
		WSPort:            0,
		ReadTimeout:       defaultServerConfigReadTimeout,
		ReadHeaderTimeout: defaultServerConfigReadHeaderTimeout,
		WriteTimeout:      defaultServerConfigWriteTimeout,
		IdleTimeout:       defaultServerConfigIdleTimeout,
	}
}

// Validate returns an error if the configuration is invalid.
func (c ServerConfig) Validate() error {
	if c.Port == 0 {
		return apperr.InvalidInput("server.port is required (got 0)")
	}

	return nil
}
