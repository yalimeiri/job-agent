package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" scheme
	_ "github.com/golang-migrate/migrate/v4/source/file"     // registers the "file" scheme
)

// Migrate applies all up migrations from migrationsPath against the database at
// databaseURL. It is safe to call on every startup: already-applied migrations
// are skipped and migrate.ErrNoChange is treated as success.
//
// migrationsPath is a filesystem path (e.g. "./migrations"); databaseURL is a
// standard postgres:// DSN which is rewritten to the pgx5:// scheme migrate's
// pgx/v5 driver expects.
func Migrate(databaseURL, migrationsPath string) error {
	src := "file://" + strings.TrimPrefix(filepathToSlash(migrationsPath), "file://")
	dbURL := toPgx5Scheme(databaseURL)

	m, err := migrate.New(src, dbURL)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// MigrateDown rolls back all migrations (used by `make migrate-down`).
func MigrateDown(databaseURL, migrationsPath string) error {
	src := "file://" + strings.TrimPrefix(filepathToSlash(migrationsPath), "file://")
	m, err := migrate.New(src, toPgx5Scheme(databaseURL))
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("rollback migrations: %w", err)
	}
	return nil
}

func toPgx5Scheme(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") {
		return "pgx5://" + strings.TrimPrefix(dsn, "postgres://")
	}
	if strings.HasPrefix(dsn, "postgresql://") {
		return "pgx5://" + strings.TrimPrefix(dsn, "postgresql://")
	}
	return dsn
}

// filepathToSlash converts OS-specific separators to forward slashes so the
// file:// URL is valid on Windows too.
func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
