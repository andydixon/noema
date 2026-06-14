package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLock is an arbitrary advisory lock key so concurrent daemons do not
// race to apply the same migration.
const migrationLock = 0x6e6f656d61 // "noema"

// Migration is one numbered schema step.
type Migration struct {
	Version  int
	Name     string
	Up, Down string
	Checksum string
}

// MigrationStatus describes one migration and whether it has been applied.
type MigrationStatus struct {
	Migration
	Applied   bool
	AppliedAt string
	Drift     bool // applied checksum differs from the embedded file
}

// LoadMigrations parses NNNN_name.up.sql / .down.sql pairs from fsys.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*Migration{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		var version int
		var rest string
		if _, err := fmt.Sscanf(name, "%d_%s", &version, &rest); err != nil {
			return nil, fmt.Errorf("migration %s: bad name", name)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		m := byVersion[version]
		if m == nil {
			m = &Migration{Version: version}
			byVersion[version] = m
		}
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			m.Name = strings.TrimSuffix(rest, ".up.sql")
			m.Up = string(body)
			sum := sha256.Sum256(body)
			m.Checksum = hex.EncodeToString(sum[:])
		case strings.HasSuffix(name, ".down.sql"):
			m.Down = string(body)
		default:
			return nil, fmt.Errorf("migration %s: must end in .up.sql or .down.sql", name)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.Up == "" {
			return nil, fmt.Errorf("migration %d has no up file", m.Version)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migrations must be contiguous from 1; got %d at position %d", m.Version, i+1)
		}
	}
	return out, nil
}

func ensureTable(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`)
	return err
}

// MigrateUp applies all pending migrations and returns the names applied.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	ms, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	var applied []string
	err = withLock(ctx, pool, func(tx pgx.Tx) error {
		if err := ensureTable(ctx, tx); err != nil {
			return err
		}
		done, err := appliedVersions(ctx, tx)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if sum, ok := done[m.Version]; ok {
				if sum != m.Checksum {
					return fmt.Errorf("migration %04d_%s was modified after being applied", m.Version, m.Name)
				}
				continue
			}
			if _, err := tx.Exec(ctx, m.Up); err != nil {
				return fmt.Errorf("migration %04d_%s: %w", m.Version, m.Name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1,$2,$3)`, m.Version, m.Name, m.Checksum); err != nil {
				return err
			}
			applied = append(applied, fmt.Sprintf("%04d_%s", m.Version, m.Name))
		}
		return nil
	})
	return applied, err
}

// MigrateDown rolls back the most recently applied migration.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (string, error) {
	ms, err := LoadMigrations(fsys)
	if err != nil {
		return "", err
	}
	var rolled string
	err = withLock(ctx, pool, func(tx pgx.Tx) error {
		if err := ensureTable(ctx, tx); err != nil {
			return err
		}
		var version int
		err := tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM schema_migrations`).Scan(&version)
		if err != nil {
			return err
		}
		if version == 0 {
			return nil
		}
		m := ms[version-1]
		if m.Down == "" {
			return fmt.Errorf("migration %04d_%s has no down file", m.Version, m.Name)
		}
		if _, err := tx.Exec(ctx, m.Down); err != nil {
			return fmt.Errorf("rollback %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, version); err != nil {
			return err
		}
		rolled = fmt.Sprintf("%04d_%s", m.Version, m.Name)
		return nil
	})
	return rolled, err
}

// Status reports every known migration and its applied state.
func Status(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]MigrationStatus, error) {
	ms, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	type row struct{ checksum, at string }
	done := map[int]row{}
	rows, err := pool.Query(ctx, `SELECT version, checksum, applied_at::text FROM schema_migrations`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var v int
			var r row
			if err := rows.Scan(&v, &r.checksum, &r.at); err != nil {
				return nil, err
			}
			done[v] = r
		}
	} // a missing table simply means nothing is applied
	out := make([]MigrationStatus, 0, len(ms))
	for _, m := range ms {
		s := MigrationStatus{Migration: m}
		if r, ok := done[m.Version]; ok {
			s.Applied, s.AppliedAt, s.Drift = true, r.at, r.checksum != m.Checksum
		}
		out = append(out, s)
	}
	return out, nil
}

func appliedVersions(ctx context.Context, tx pgx.Tx) (map[int]string, error) {
	rows, err := tx.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}

func withLock(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return Tx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLock)); err != nil {
			return fmt.Errorf("migration lock: %w", err)
		}
		return fn(tx)
	})
}
