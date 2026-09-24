package archive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lsm/internal/store"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

// Every table a migration creates must be in the archive, or it would be
// silently missing from backups.
func TestEveryTableIsArchived(t *testing.T) {
	db := newDB(t)
	rows, err := db.Query(`SELECT name FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(Tables, name) {
			t.Errorf("table %q is not listed in archive.Tables", name)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newDB(t)
	exec(t, src,
		// Reparent a role under one created after it, so its rowid (export
		// order) precedes its parent's: restore must not depend on row
		// order within a self-referencing table.
		`INSERT INTO roles (id, parent_id, name) VALUES
		 ('00000000-0000-7000-8000-0000000000a0', NULL, 'Vendor')`,
		`INSERT INTO roles (id, parent_id, name) VALUES
		 ('00000000-0000-7000-8000-0000000000b1', '00000000-0000-7000-8000-0000000000a0', 'Windy''s')`,
		`INSERT INTO roles (id, parent_id, name) VALUES
		 ('00000000-0000-7000-8000-0000000000b0', NULL, 'Vendor/Concessions')`,
		`UPDATE roles SET parent_id = '00000000-0000-7000-8000-0000000000b0'
		 WHERE id = '00000000-0000-7000-8000-0000000000b1'`,
		`INSERT INTO persons (id, badge, name, role_id, hire_date, gender, created_at, updated_at) VALUES
		 ('00000000-0000-7000-8000-0000000000c0', '100000', 'Jason Figge', '00000000-0000-7000-8000-0000000000b1',
		  '2024-08-24', 'male', '2026-09-23T00:00:00.000000Z', '2026-09-23T00:00:00.000000Z')`,
		`INSERT INTO resources (id, name, tracked) VALUES ('00000000-0000-7000-8000-0000000000d0', 'Radio', 1)`,
		`INSERT INTO settings (key, value, updated_at) VALUES ('backup', '{"cron":"0 3 * * *"}', 't')`,
	)

	var first bytes.Buffer
	if err := Export(ctx, src, &first); err != nil {
		t.Fatal(err)
	}

	dst := newDB(t)
	exec(t, dst, `INSERT INTO resources (id, name) VALUES ('00000000-0000-7000-8000-0000000000ee', 'Doomed')`)
	sum, err := RestoreOverwrite(ctx, dst, bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Rows["roles"] != 3 || sum.Rows["persons"] != 1 {
		t.Fatalf("summary = %v", sum.Rows)
	}

	var second bytes.Buffer
	if err := Export(ctx, dst, &second); err != nil {
		t.Fatal(err)
	}
	// Byte-identical apart from the export timestamp line.
	strip := func(b []byte) []byte {
		var out [][]byte
		for _, l := range bytes.Split(b, []byte("\n")) {
			if !bytes.Contains(l, []byte(`"exported_at"`)) {
				out = append(out, l)
			}
		}
		return bytes.Join(out, []byte("\n"))
	}
	if !bytes.Equal(strip(first.Bytes()), strip(second.Bytes())) {
		t.Fatalf("round trip differs:\n%s\n---\n%s", first.String(), second.String())
	}

	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM resources WHERE name = 'Doomed'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("overwrite left pre-existing rows: n=%d err=%v", n, err)
	}
}

func TestRestoreIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	exec(t, db, `INSERT INTO resources (id, name) VALUES ('00000000-0000-7000-8000-0000000000ee', 'Survivor')`)

	// A dangling foreign key fails at commit; nothing may change.
	bad := `{"format":"lsm-archive","format_version":1,"schema_version":"CURRENT","exported_at":"x","tables":[
	  {"name":"persons","rows":[{"id":"00000000-0000-7000-8000-000000000001","badge":"1","name":"x",
	   "role_id":"00000000-0000-7000-8000-00000000dead","hire_date":"2020-01-01","created_at":"t","updated_at":"t"}]}]}`
	if _, err := RestoreOverwrite(ctx, db, archiveJSON(t, bad)); err == nil {
		t.Fatal("restore with dangling foreign key succeeded")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM resources`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed restore changed data: n=%d err=%v", n, err)
	}
}

func TestRestoreRejects(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	cases := map[string]string{
		"wrong format":   `{"format":"abi","format_version":1,"schema_version":"CURRENT","tables":[]}`,
		"unknown table":  `{"format":"lsm-archive","format_version":1,"schema_version":"CURRENT","tables":[{"name":"sqlite_master","rows":[]}]}`,
		"unknown column": `{"format":"lsm-archive","format_version":1,"schema_version":"CURRENT","tables":[{"name":"settings","rows":[{"key":"k","value":"v","updated_at":"t","x\"); DROP TABLE roles; --":1}]}]}`,
	}
	for name, in := range cases {
		if _, err := RestoreOverwrite(ctx, db, archiveJSON(t, in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	old := `{"format":"lsm-archive","format_version":1,"schema_version":"0000","tables":[]}`
	if _, err := RestoreOverwrite(ctx, db, bytes.NewBufferString(old)); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("schema mismatch: err = %v", err)
	}
}

// archiveJSON substitutes the current schema version for CURRENT, so a
// hand-written archive fails for the reason under test rather than a
// version mismatch.
func archiveJSON(t *testing.T, s string) *bytes.Buffer {
	t.Helper()
	all, err := store.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBufferString(strings.ReplaceAll(s, `"CURRENT"`, `"`+all[len(all)-1].Version+`"`))
}

func exec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}
