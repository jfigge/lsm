package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// SPEC §2 "Depth charts" worked example: primary on the 112 landing and
// tertiary on the 118 landing sets the hidden landings preference, and a
// reshuffle on 118 must not strip what 112 earned.
func TestDepthDerivedPreference(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	const (
		role    = "00000000-0000-7000-8000-000000000001"
		person  = "00000000-0000-7000-8000-000000000002"
		landing = "00000000-0000-7000-8000-000000000003"
		p112    = "00000000-0000-7000-8000-000000000004"
		p118    = "00000000-0000-7000-8000-000000000005"
	)
	mustExec(t, db,
		`INSERT INTO roles (id, name) VALUES ('`+role+`', 'Event Security')`,
		`INSERT INTO persons (id, badge, name, role_id, hire_date, created_at, updated_at)
		 VALUES ('`+person+`', '100000', 'P', '`+role+`', '2022-01-01', 't', 't')`,
		`INSERT INTO capabilities (id, name, staff_selectable) VALUES ('`+landing+`', 'landing', 0)`,
		`INSERT INTO positions (id, name, department_id, shift_pattern) VALUES
		 ('`+p112+`', '112 Landing', '`+role+`', 'full_session'),
		 ('`+p118+`', '118 Landing', '`+role+`', 'full_session')`,
		`INSERT INTO position_capabilities VALUES ('`+p112+`', '`+landing+`'), ('`+p118+`', '`+landing+`')`,
		`INSERT INTO position_queue (position_id, person_id, depth, added_at) VALUES
		 ('`+p112+`', '`+person+`', 1, 't'), ('`+p118+`', '`+person+`', 3, 't')`,
	)

	state := func() string {
		var s string
		err := db.QueryRow(`SELECT state FROM person_capability_effective
			WHERE person_id = ? AND capability_id = ?`, person, landing).Scan(&s)
		if err == sql.ErrNoRows {
			return "no_preference"
		}
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if got := state(); got != "yes" {
		t.Fatalf("primary on 112: state = %s, want yes", got)
	}
	mustExec(t, db, `UPDATE position_queue SET depth = 4 WHERE position_id = '`+p118+`'`)
	if got := state(); got != "yes" {
		t.Fatalf("after 118 reshuffle: state = %s, want yes (earned on 112)", got)
	}
	mustExec(t, db, `UPDATE position_queue SET depth = 5 WHERE position_id = '`+p112+`'`)
	if got := state(); got != "no_preference" {
		t.Fatalf("no qualifying rank left: state = %s, want no_preference", got)
	}
	mustExec(t, db,
		`UPDATE position_queue SET depth = 1 WHERE position_id = '`+p112+`'`,
		`INSERT INTO person_capabilities (person_id, capability_id, state, updated_at)
		 VALUES ('`+person+`', '`+landing+`', 'restricted', 't')`)
	if got := state(); got != "restricted" {
		t.Fatalf("restricted must win over depth: state = %s", got)
	}
}

func mustExec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}
