package seed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lsm/internal/archive"
	"lsm/internal/store"
)

// demoDir is the repository's seed directory.
const demoDir = "../../seed"

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

func venue(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestDemoSeed loads the real seed once and checks it from several angles;
// a full load is slow under -race.
func TestDemoSeed(t *testing.T) {
	db := newDB(t)
	sum, err := Load(context.Background(), db, demoDir, venue(t))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Inserted() == 0 {
		t.Fatal("nothing inserted")
	}
	t.Run("counts", func(t *testing.T) { checkCounts(t, db) })
	t.Run("positions", func(t *testing.T) { checkPositions(t, db) })
	t.Run("credentials", func(t *testing.T) { checkCredentials(t, db) })
	t.Run("idempotent and archivable", func(t *testing.T) { checkRerunAndArchive(t, db) })
}

func checkCounts(t *testing.T, db *sql.DB) {

	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM persons`:                                           521,
		`SELECT COUNT(*) FROM persons WHERE tier = 'staff'`:                      494,
		`SELECT COUNT(*) FROM persons WHERE tier = 'supervisor'`:                 24,
		`SELECT COUNT(*) FROM persons WHERE tier = 'admin'`:                      3,
		`SELECT COUNT(*) FROM persons WHERE supervisor_id IS NOT NULL`:           0,
		`SELECT COUNT(*) FROM persons WHERE badge = '100000' AND tier = 'admin'`: 1,
		`SELECT COUNT(*) FROM person_capabilities WHERE state = 'restricted'`:    55,
		`SELECT COUNT(*) FROM person_capabilities WHERE state = 'no_preference'`: 0,
		`SELECT COUNT(*) FROM events`:                                            43,
		`SELECT COUNT(*) FROM shift_windows`:                                     172,
		`SELECT COUNT(*) FROM schedule_months`:                                   4,
		`SELECT COUNT(*) FROM schedule_months WHERE status = 'open'`:             1,
		`SELECT COUNT(*) FROM availability`:                                      22403,
		`SELECT COUNT(*) FROM rating_entries`:                                    9184,
		`SELECT COUNT(*) FROM ratings_current`:                                   9184,
		`SELECT COUNT(*) FROM resource_instances`:                                40,
		// No event may span midnight UTC wrongly: 13:30 EDT is 17:30Z.
		`SELECT COUNT(*) FROM shift_windows WHERE substr(start_at, 12, 5) NOT IN ('17:30','18:00','18:30','19:00')`: 0,
	} {
		if got := count(t, db, query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}

// The door-set and second-shift figures SPEC §5 quotes as reference
// numbers must fall out of the seeded posts, not be hardcoded anywhere.
func checkPositions(t *testing.T, db *sql.DB) {
	const security = `department_id = (SELECT id FROM roles WHERE name = 'Event Security' AND parent_id IS NULL)`
	lanes := `(area LIKE 'East %' OR area LIKE 'West %' OR area LIKE 'South %') AND area NOT LIKE '%Tickets' AND area NOT LIKE '%Lounge'`
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM positions WHERE ` + security + ` AND ` + lanes:                                           45,
		`SELECT COUNT(*) FROM positions WHERE ` + security + ` AND ` + lanes + ` AND shift_pattern = 'full_session'`:   6,
		`SELECT COUNT(*) FROM positions WHERE ` + security + ` AND ` + lanes + ` AND shift_pattern = 'two_shift_door'`: 39,
		`SELECT COUNT(*) FROM positions WHERE ` + security + ` AND shift_pattern = 'second_shift_only'`:                30,
		`SELECT COUNT(*) FROM positions WHERE pairing_rule = 'mixed_gender'`:                                           12,
		`SELECT COUNT(*) FROM positions WHERE pairing_rule = 'mixed_gender' AND needs_breaking = 1`:                    0,
		`SELECT COUNT(*) FROM positions WHERE min_tenure_half_years IS NOT NULL`:                                       4,
		`SELECT COUNT(*) FROM positions WHERE is_lead = 1`:                                                             8,
		// Every lead, every landing and each lane's Door 1 carries a radio.
		`SELECT COUNT(*) FROM positions p WHERE (is_lead = 1 OR name LIKE '% Landing' OR (name LIKE '% Door 1' AND ` + lanes + `))
		 AND NOT EXISTS (SELECT 1 FROM position_resources pr JOIN resources r ON r.id = pr.resource_id
		                 WHERE pr.position_id = p.id AND r.name = 'Radio')`: 0,
		// Every position applies to at least one event type.
		`SELECT COUNT(*) FROM positions p WHERE NOT EXISTS (SELECT 1 FROM position_event_types WHERE position_id = p.id)`: 0,
	} {
		if got := count(t, db, query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
	rows, err := db.Query(`SELECT p.name, COUNT(*) FROM position_lead_pool lp
		JOIN positions p ON p.id = lp.lead_position_id WHERE p.name NOT LIKE '%Lounge%' GROUP BY p.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	leads := 0
	for rows.Next() {
		var (
			name string
			n    int
		)
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		leads++
		if n != 6 && n != 7 {
			t.Errorf("%s leads %d posts, want 6 or 7", name, n)
		}
	}
	if leads != 6 {
		t.Errorf("%d lane leads, want 6", leads)
	}
}

func checkCredentials(t *testing.T, db *sql.DB) {
	if got := count(t, db, `SELECT COUNT(*) FROM credentials WHERE must_change_password = 1`); got != 521 {
		t.Errorf("credentials forcing a reset = %d, want 521", got)
	}

	var recs []struct {
		Badge           string `json:"badge"`
		InitialPassword string `json:"initial_password"`
	}
	b, err := os.ReadFile(filepath.Join(demoDir, "staff.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs[:25] {
		var hash string
		if err := db.QueryRow(`SELECT c.password_hash FROM credentials c JOIN persons p ON p.id = c.person_id
			WHERE p.badge = ?`, r.Badge).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(r.InitialPassword))
		if hash != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Errorf("badge %s: stored hash does not verify against the seed password", r.Badge)
		}
	}

	// The plaintext demo password must not exist anywhere in the system.
	var dump bytes.Buffer
	if err := archive.Export(context.Background(), db, &dump); err != nil {
		t.Fatal(err)
	}
	var a archive.Archive
	if err := json.Unmarshal(dump.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	stored := map[string]bool{}
	for _, section := range a.Tables {
		for _, row := range section.Rows {
			for _, v := range row {
				if s, ok := v.(string); ok {
					stored[s] = true
				}
			}
		}
	}
	for _, r := range recs {
		if stored[r.InitialPassword] {
			t.Fatalf("initial_password of %s found in the database", r.Badge)
		}
	}
}

// Re-running the seed changes nothing, and a seeded database survives the
// archive round trip unchanged.
func checkRerunAndArchive(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	var first bytes.Buffer
	if err := archive.Export(ctx, db, &first); err != nil {
		t.Fatal(err)
	}

	sum, err := Load(ctx, db, demoDir, venue(t))
	if err != nil {
		t.Fatal(err)
	}
	if n := sum.Inserted(); n != 0 {
		t.Fatalf("second load inserted %d rows: %+v", n, sum)
	}

	dst := newDB(t)
	if _, err := archive.RestoreOverwrite(ctx, dst, bytes.NewReader(first.Bytes())); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := archive.Export(ctx, dst, &second); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("seeded database changed across export and restore")
	}
}

// A bad seed is reported and loads nothing at all.
func TestLoadRejects(t *testing.T) {
	const (
		event  = "11111111-1111-4111-8111-111111111111"
		window = "22222222-2222-4222-8222-222222222222"
		other  = "33333333-3333-4333-8333-333333333333"
		otherW = "44444444-4444-4444-8444-444444444444"
	)
	base := map[string]string{
		"catalog.json": `{"roles":[{"name":"Event Security"}],
			"event_types":[{"name":"Hockey","match":["Hurricanes vs "]}],
			"capabilities":[{"name":"doors","staff_selectable":true,"roles":["Event Security"]}],
			"resources":[]}`,
		"positions.json": `[{"name":"East Fast Door 1","department":"Event Security","shift_pattern":"full_session","capabilities":["doors"]}]`,
		"staff.json": `[{"badge":"1","name":"A","role":"Event Security","sub_role":null,"hire_date":"2024-01-01",
			"capabilities":{"doors":"yes"},"gender":"female","tier":"staff","email":"a@x","password_hash":"sha256:ab","must_change_password":true}]`,
		"events.json": `[
			{"id":"` + event + `","date":"2026-10-03","name":"Hurricanes vs Ottawa Senators","month":"2026-10","status":"open",
			 "shift_windows":[{"id":"` + window + `","start":"13:30","end":"21:30"}]},
			{"id":"` + other + `","date":"2026-10-08","name":"Hurricanes vs Detroit Red Wings","month":"2026-10","status":"open",
			 "shift_windows":[{"id":"` + otherW + `","start":"13:30","end":"21:30"}]}]`,
		"availability.json": `[{"person_badge":"1","event_id":"` + event + `","window_id":"` + window + `","status":"window"}]`,
		"ratings.json":      `[]`,
	}

	write := func(t *testing.T, override map[string]string) string {
		dir := t.TempDir()
		for name, body := range base {
			if o, ok := override[name]; ok {
				body = o
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	// The fixture itself is valid.
	if _, err := Load(context.Background(), newDB(t), write(t, nil), venue(t)); err != nil {
		t.Fatalf("base fixture: %v", err)
	}

	cases := map[string]map[string]string{
		"unknown badge":             {"availability.json": `[{"person_badge":"999","event_id":"` + event + `","window_id":null,"status":"all shifts"}]`},
		"window from another event": {"availability.json": `[{"person_badge":"1","event_id":"` + event + `","window_id":"` + otherW + `","status":"window"}]`},
		"unmatched event name": {"events.json": `[{"id":"` + event + `","date":"2026-10-03","name":"Mystery Night","month":"2026-10","status":"open",
			"shift_windows":[{"id":"` + window + `","start":"13:30","end":"21:30"}]}]`},
		"mixed month status":    {"events.json": strings.Replace(base["events.json"], `"status":"open"`, `"status":"closed"`, 1)},
		"unknown capability":    {"positions.json": `[{"name":"X","department":"Event Security","shift_pattern":"full_session","capabilities":["juggling"]}]`},
		"typo in authored file": {"positions.json": `[{"name":"X","department":"Event Security","shift_patern":"full_session"}]`},
		"invalid rating":        {"ratings.json": `[{"person_badge":"1","event_id":"` + event + `","rating":"meh","rated_by_badge":"1","rated_at":"2026-10-03T22:15:00-04:00","source":"seed"}]`},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			db := newDB(t)
			// Migrations may ship reference rows (ranking weights); the
			// failed load must leave exactly what migrating left.
			before := map[string]int{}
			for _, table := range archive.Tables {
				before[table] = count(t, db, `SELECT COUNT(*) FROM "`+table+`"`)
			}
			if _, err := Load(context.Background(), db, write(t, override), venue(t)); err == nil {
				t.Fatal("load succeeded")
			}
			for _, table := range archive.Tables {
				if n := count(t, db, `SELECT COUNT(*) FROM "`+table+`"`); n != before[table] {
					t.Errorf("failed load changed %s: %d rows, was %d", table, n, before[table])
				}
			}
		})
	}
}
