package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// NewDatabasePool applies pending migrations, then opens the connection pool,
// so the app never runs against an outdated schema.
func NewDatabasePool(ctx context.Context, databaseURL string, logger *slog.Logger) (*pgxpool.Pool, func(), error) {
	if err := migrateUp(databaseURL, logger); err != nil {
		return nil, nil, err
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing database config: %w", err)
	}

	// Keep HNSW scanning past rows that the Corpus filter drops (pgvector 0.8+;
	// older versions ignore the setting). See the SearchCorpus query.
	config.ConnConfig.RuntimeParams["hnsw.iterative_scan"] = "relaxed_order"

	config.MaxConns = 15
	config.MinConns = 2
	config.MaxConnLifetime = 1 * time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, nil, fmt.Errorf("error connecting to the postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("database did not respond to ping: %w", err)
	}

	logger.Info("Connection to PostgreSQL (pgxpool) successfully established!")

	cleanup := func() {
		logger.Info("Closing connection pool with Postgres")
		pool.Close()
	}

	return pool, cleanup, nil
}

func migrateUp(databaseURL string, logger *slog.Logger) error {
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("loading embedded migrations: %w", err)
	}

	// golang-migrate picks its driver by URL scheme; pgx/v5 registers as pgx5.
	migrateURL := "pgx5://" + strings.TrimPrefix(strings.TrimPrefix(databaseURL, "postgresql://"), "postgres://")

	m, err := migrate.NewWithSourceInstance("iofs", source, migrateURL)
	if err != nil {
		return fmt.Errorf("preparing migrations: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", err)
	}

	version, _, _ := m.Version()
	logger.Info("Database schema is up to date", "version", version)
	return nil
}
