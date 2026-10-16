package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies the embedded, numbered migrations and records them in the same
// transaction. A database advisory lock also makes concurrent service starts safe.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(-762847264036112790)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
	)`); err != nil {
		return fmt.Errorf("create migration tracking: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Strings(names)
	seen := make(map[int]bool)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		prefix, _, ok := strings.Cut(base, "_")
		version, parseErr := strconv.Atoi(prefix)
		if !ok || parseErr != nil || version < 1 || seen[version] {
			return fmt.Errorf("invalid migration name: %s", base)
		}
		seen[version] = true
		source, readErr := migrations.ReadFile(name)
		if readErr != nil {
			return fmt.Errorf("read migration %s: %w", base, readErr)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(source))
		var savedName, savedChecksum string
		err = tx.QueryRowContext(ctx, "SELECT name, checksum FROM schema_migrations WHERE version = $1", version).Scan(&savedName, &savedChecksum)
		if err == nil {
			if savedName != base || savedChecksum != checksum {
				return fmt.Errorf("applied migration %d has changed", version)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("inspect migration %s: %w", base, err)
		}
		if _, err = tx.ExecContext(ctx, string(source)); err != nil {
			return fmt.Errorf("apply migration %s: %w", base, err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, checksum) VALUES($1, $2, $3)", version, base, checksum); err != nil {
			return fmt.Errorf("record migration %s: %w", base, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
