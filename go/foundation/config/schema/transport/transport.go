// Package transport is the config surface for the edge (transport) layer:
// WebSocket, gRPC, CORS and HTTP tuning. Defaults are permissive (CORS allows all
// origins), so a service opts into each restriction through its overlays; a controller's
// rate limit is a decoration layer (decoration.controllers), not a key here. Pure data
// (mirrors infra/S3Config); the consumer's providers convert it into the
// server/middleware wiring.
package transport

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// WSConfig tunes the WebSocket server, with bounded idle, write and ping defaults a
// deployer can tune.
type WSConfig struct {
	// AllowedOrigins lists the request Origins accepted on the WebSocket handshake
	// when AllowInsecureOrigins is false. Behind a gateway (staging/prod) the
	// browser's Origin (e.g. app.example.com) differs from the upstream Host
	// (e.g. ws.app.svc.cluster.local:8080), so the coder/websocket default
	// Origin==Host check rejects every upgrade with 403; listing the frontend
	// origin(s) here passes them through as the Accept OriginPatterns. Empty keeps
	// the strict same-origin default. Patterns may include a wildcard subdomain
	// (e.g. "*.example.com").
	AllowedOrigins []string `mapstructure:"allowed_origins"`

	// IdleTimeout closes a connection idle for longer than this (0 = no timeout).
	IdleTimeout time.Duration `mapstructure:"idle_timeout"`

	// WriteDeadline bounds a single message write (0 = no deadline).
	WriteDeadline time.Duration `mapstructure:"write_deadline"`

	// PingInterval is the server keepalive ping cadence (0 = disabled).
	PingInterval time.Duration `mapstructure:"ping_interval"`

	// PongTimeout bounds how long a heartbeat ping waits for its pong before the socket is
	// declared dead and closed. 0 = wait forever (heartbeat effectively disabled).
	PongTimeout time.Duration `mapstructure:"pong_timeout"`

	// MaxMessageBytes caps an inbound WS message size; the WS server threads it into
	// SetReadLimit, so an oversize frame triggers a 1009 close (0 = library default).
	MaxMessageBytes int64 `mapstructure:"max_message_bytes"`

	// MaxConnections caps the total concurrent WebSocket connections the hub accepts
	// on this machine (0 = unlimited).
	MaxConnections int `mapstructure:"max_connections"`

	// MaxConnectionsPerUser caps concurrent connections owned by a single user
	// (0 = unlimited).
	MaxConnectionsPerUser int `mapstructure:"max_connections_per_user"`

	// AllowInsecureOrigins disables the handshake Origin check (local dev only);
	// never enable it outside local development.
	AllowInsecureOrigins bool `mapstructure:"allow_insecure_origins"`
}

// GRPCConfig tunes the gRPC/Connect client + server keepalive and message size.
type GRPCConfig struct {
	// KeepaliveTime is the client keepalive ping cadence (0 = library default).
	KeepaliveTime time.Duration `mapstructure:"keepalive_time"`

	// KeepaliveTimeout is how long a keepalive ping waits for an ack.
	KeepaliveTimeout time.Duration `mapstructure:"keepalive_timeout"`

	// MaxMessageBytes caps a gRPC message size (0 = library default).
	MaxMessageBytes int64 `mapstructure:"max_message_bytes"`
}

// CORSConfig tunes cross-origin sharing. Defaults are permissive (allow all); a
// production overlay should restrict allow_origins.
type CORSConfig struct {
	// MaxAge is how long (seconds) browsers may cache a preflight result.
	MaxAge string `mapstructure:"max_age"`

	// AllowOrigins lists permitted origins; ["*"] allows all (the default).
	AllowOrigins []string `mapstructure:"allow_origins"`
}

// Config is the edge-layer config surface.
type Config struct {
	// CORS tunes cross-origin sharing.
	CORS CORSConfig `mapstructure:"cors"`

	// WS tunes the WebSocket server.
	WS WSConfig `mapstructure:"ws"`

	// GRPC tunes the gRPC/Connect keepalive + message size.
	GRPC GRPCConfig `mapstructure:"grpc"`

	// MaxHeaderBytes caps the HTTP request header size (Go's own default is 1 MiB).
	MaxHeaderBytes int `mapstructure:"max_header_bytes"`
}

// DefaultConfig returns permissive defaults — CORS allowing all origins — with
// sensible bounds for the WS/HTTP knobs.
func DefaultConfig() Config {
	return Config{
		WS: WSConfig{
			// 5m suits persistent connections that idle between messages.
			IdleTimeout:   5 * time.Minute,
			WriteDeadline: 10 * time.Second,
			PingInterval:  30 * time.Second,
			PongTimeout:   10 * time.Second,
			// 64 KiB is the WS read limit: an oversize inbound frame triggers a
			// 1009 close. The WS server threads this into SetReadLimit (it is the WS-specific
			// message cap, not the larger generic HTTP body limit).
			MaxMessageBytes:       64 << 10,
			MaxConnections:        10000,
			MaxConnectionsPerUser: 20,
		},
		GRPC: GRPCConfig{
			KeepaliveTime:    30 * time.Second,
			KeepaliveTimeout: 10 * time.Second,
			MaxMessageBytes:  4 << 20,
		},
		CORS: CORSConfig{
			AllowOrigins: []string{"*"},
			MaxAge:       "3600",
		},
		MaxHeaderBytes: 1 << 20,
	}
}

// Validate rejects negative durations/sizes.
func (c Config) Validate() error {
	if c.WS.IdleTimeout < 0 || c.WS.WriteDeadline < 0 || c.WS.PingInterval < 0 ||
		c.WS.PongTimeout < 0 {
		return apperr.InvalidInput("transport.ws timeouts must be >= 0")
	}
	if c.WS.MaxConnections < 0 || c.WS.MaxConnectionsPerUser < 0 {
		return apperr.InvalidInput("transport.ws connection limits must be >= 0")
	}
	if c.MaxHeaderBytes < 0 {
		return apperr.InvalidInput("transport.max_header_bytes must be >= 0")
	}
	return nil
}
