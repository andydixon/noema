package storage

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/andydixon/noema/migrations"
)

func TestLoadMigrationsValidation(t *testing.T) {
	if _, err := LoadMigrations(migrations.FS); err != nil {
		t.Fatalf("embedded migrations invalid: %v", err)
	}
	bad := fstest.MapFS{"0002_x.up.sql": {Data: []byte("select 1")}}
	if _, err := LoadMigrations(bad); err == nil {
		t.Fatal("expected contiguity error")
	}
}

func TestMigrateUpDownAndAppendOnly(t *testing.T) {
	pool := TestPool(t, migrations.FS)
	ctx := context.Background()
	st, err := Status(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st {
		if !s.Applied || s.Drift {
			t.Fatalf("migration %d not cleanly applied: %+v", s.Version, s)
		}
	}
	// Append-only trigger on audit_log.
	if _, err := pool.Exec(ctx, `INSERT INTO audit_log (id, actor_kind, action) VALUES (gen_random_uuid(),'system','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action='x'`); err == nil {
		t.Fatal("update on audit_log should be rejected")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log`); err == nil {
		t.Fatal("delete on audit_log should be rejected")
	}
	// Roll everything back and forward again.
	for i := len(st); i > 0; i-- {
		if _, err := MigrateDown(ctx, pool, migrations.FS); err != nil {
			t.Fatalf("down %d: %v", i, err)
		}
	}
	applied, err := MigrateUp(ctx, pool, migrations.FS)
	if err != nil || len(applied) != len(st) {
		t.Fatalf("re-apply: %v (%d)", err, len(applied))
	}
}
