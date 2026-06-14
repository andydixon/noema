package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPool returns a pool bound to a fresh schema with all migrations applied.
// It skips the test when NOEMA_TEST_DATABASE_URL is unset. Each test gets its
// own schema so tests may run in parallel and leave nothing behind.
func TestPool(t testing.TB, migrations fs.FS) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("NOEMA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NOEMA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	var b [6]byte
	rand.Read(b[:])
	schema := "t_" + hex.EncodeToString(b[:])

	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams = map[string]string{"search_path": schema + ",public"}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema: %v", err)
	}
	if _, err := MigrateUp(ctx, pool, migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck
		admin.Close()
	})
	return pool
}
