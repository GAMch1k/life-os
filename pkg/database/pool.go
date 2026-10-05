// Package database contains domain-neutral PostgreSQL infrastructure shared by
// Life OS services. Database schemas, migrations, and queries belong to the
// individual services, never to this package.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config describes a service's PostgreSQL connection pool. Zero values leave
// pgx defaults intact, except ConnectTimeout which defaults to five seconds.
// The tags make Config directly loadable by the shared config loader.
type Config struct {
	URL               string        `env:"DATABASE_URL" required:"true"`
	ApplicationName   string        `env:"DATABASE_APPLICATION_NAME"`
	MinConnections    int32         `env:"DATABASE_MIN_CONNECTIONS" default:"0"`
	MaxConnections    int32         `env:"DATABASE_MAX_CONNECTIONS" default:"0"`
	MaxConnectionIdle time.Duration `env:"DATABASE_MAX_CONNECTION_IDLE" default:"0s"`
	MaxConnectionLife time.Duration `env:"DATABASE_MAX_CONNECTION_LIFETIME" default:"0s"`
	HealthCheckPeriod time.Duration `env:"DATABASE_HEALTH_CHECK_PERIOD" default:"0s"`
	ConnectTimeout    time.Duration `env:"DATABASE_CONNECT_TIMEOUT" default:"5s"`
}

// Validate checks constraints which pgx otherwise reports only when the pool
// is created or first used.
func (c Config) Validate() error {
	if c.URL == "" {
		return errors.New("database URL is required")
	}
	if c.MinConnections < 0 {
		return errors.New("minimum connections must not be negative")
	}
	if c.MaxConnections < 0 {
		return errors.New("maximum connections must not be negative")
	}
	if c.MaxConnections > 0 && c.MinConnections > c.MaxConnections {
		return errors.New("minimum connections must not exceed maximum connections")
	}
	if c.MaxConnectionIdle < 0 || c.MaxConnectionLife < 0 || c.HealthCheckPeriod < 0 || c.ConnectTimeout < 0 {
		return errors.New("database durations must not be negative")
	}
	return nil
}

// ParsePoolConfig converts Config into a native pgx pool configuration. The
// returned value remains available for service-specific pgx customization.
func ParsePoolConfig(config Config) (*pgxpool.Config, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("database: invalid pool configuration: %w", err)
	}

	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		return nil, fmt.Errorf("database: parse connection configuration: %w", err)
	}
	if config.ApplicationName != "" {
		poolConfig.ConnConfig.RuntimeParams["application_name"] = config.ApplicationName
	}
	if config.MinConnections > 0 {
		poolConfig.MinConns = config.MinConnections
	}
	if config.MaxConnections > 0 {
		poolConfig.MaxConns = config.MaxConnections
	}
	if config.MaxConnectionIdle > 0 {
		poolConfig.MaxConnIdleTime = config.MaxConnectionIdle
	}
	if config.MaxConnectionLife > 0 {
		poolConfig.MaxConnLifetime = config.MaxConnectionLife
	}
	if config.HealthCheckPeriod > 0 {
		poolConfig.HealthCheckPeriod = config.HealthCheckPeriod
	}
	connectTimeout := config.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = 5 * time.Second
	}
	poolConfig.ConnConfig.ConnectTimeout = connectTimeout
	return poolConfig, nil
}

// Open creates a pool and verifies connectivity before returning it. A failed
// ping closes all pool resources before the error is returned.
func Open(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	poolConfig, err := ParsePoolConfig(config)
	if err != nil {
		return nil, err
	}
	return OpenConfig(ctx, poolConfig)
}

// OpenConfig creates and verifies a pool from a native pgx configuration.
func OpenConfig(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	if ctx == nil {
		return nil, errors.New("database: open context must not be nil")
	}
	if config == nil {
		return nil, errors.New("database: pgx pool configuration must not be nil")
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("database: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}
	return pool, nil
}

// ClosePool adapts a pgx pool to httpserver.App.OnShutdown without coupling the
// database package to the HTTP runtime. pgx waits for acquired connections, so
// handlers must release rows and connections before shutdown completes.
func ClosePool(pool *pgxpool.Pool) func(context.Context) error {
	return func(context.Context) error {
		if pool != nil {
			pool.Close()
		}
		return nil
	}
}
