package infra

import "fmt"

// defaultRedisConfigPort is the default Port of RedisConfig.
const defaultRedisConfigPort = 6379

// defaultRedisConfigPoolSize is the default PoolSize of RedisConfig.
const defaultRedisConfigPoolSize = 100

// defaultRedisConfigMinIdleConns is the default MinIdleConns of RedisConfig.
const defaultRedisConfigMinIdleConns = 5

// RedisConfig holds Redis connection configuration.
type RedisConfig struct {
	// Host is the Redis server hostname or IP address.
	Host string `mapstructure:"host" envalias:"REDIS_HOST"`

	// Password is the Redis AUTH password; empty string disables authentication.
	Password string `mapstructure:"password" envalias:"REDIS_PASSWORD"`

	// FailureMode controls cache behavior on Redis errors ("bypass" silently skips the
	// cache).
	FailureMode string `mapstructure:"failure_mode"`

	// Port is the Redis server port (default 6379).
	Port int `mapstructure:"port" envalias:"REDIS_PORT"`

	// DB is the Redis database index to select (0-15).
	DB int `mapstructure:"db"`

	// PoolSize is the maximum number of socket connections in the client pool.
	PoolSize int `mapstructure:"pool_size"`

	// MinIdleConns is the minimum number of idle connections maintained in the pool.
	MinIdleConns int `mapstructure:"min_idle_conns"`

	// TLS enables an encrypted connection. Managed Redis such as AWS ElastiCache
	// with an auth token requires in-transit encryption; a plaintext client hangs
	// and i/o-times-out against it. False for local dev Redis.
	TLS bool `mapstructure:"tls"`
}

// DefaultRedisConfig returns a RedisConfig with defaults for local dev.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Host:         "localhost",
		Password:     "",
		FailureMode:  "bypass",
		Port:         defaultRedisConfigPort,
		DB:           0,
		PoolSize:     defaultRedisConfigPoolSize,
		MinIdleConns: defaultRedisConfigMinIdleConns,
		TLS:          false,
	}
}

// Addr returns the Redis address in "host:port" format.
func (c *RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// Validate returns an error if the configuration is invalid.
func (*RedisConfig) Validate() error {
	return nil
}
