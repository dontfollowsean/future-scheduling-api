package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrations holds the SQL files, so the binary carries its own schema.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies every pending migration and returns the schema version
// the database is now at. Running it again applies nothing.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return 0, err
	}

	// goose works on database/sql. This *sql.DB borrows connections from
	// pool and keeps none idle, so closing it leaves pool open and has no
	// error worth reporting.
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, dir)
	if err != nil {
		return 0, fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}
