package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first, err := Migrate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("no migrations applied to an empty database")
	}
	second, err := Migrate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second run applied %v", second)
	}
}

func TestMigrateRejectsEditedMigration(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET checksum = 'x' WHERE version = '0001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, db); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO persons (id, badge, name, role_id, hire_date, created_at, updated_at)
		VALUES ('0190f000-0000-7000-8000-000000000001', '1', 'x', '0190f000-0000-7000-8000-00000000dead', '2020-01-01', 'now', 'now')`)
	if err == nil {
		t.Fatal("insert with dangling role_id succeeded; foreign keys are off")
	}
}
