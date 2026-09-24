// Package archive exports every entity to a JSON archive and restores from
// one. The archive is the backup format and the migration path off ABI
// (SPEC §1, §6).
//
// The archive is table-shaped: one section per table, one JSON object per
// row, keyed by column name. Because every entity is keyed by UUID, an
// archive imports into any database without id collisions.
package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// Format identifies an LSM archive; FormatVersion changes only if the
// envelope itself changes (the schema is versioned separately).
const (
	Format        = "lsm-archive"
	FormatVersion = 1
)

// Tables lists every persisted table in foreign-key dependency order:
// parents before children. Export writes in this order and a full restore
// deletes in reverse. A test fails if a migration adds a table that is not
// listed here, so no entity can silently fall out of the backup.
var Tables = []string{
	"roles",
	"persons",
	"credentials",
	"sessions",
	"capabilities",
	"capability_roles",
	"person_capabilities",
	"restriction_notes",
	"event_types",
	"events",
	"schedule_months",
	"shift_windows",
	"availability",
	"rating_entries",
	"resources",
	"resource_instances",
	"positions",
	"position_capabilities",
	"position_event_types",
	"position_resources",
	"position_lead_pool",
	"position_queue",
	"event_staff",
	"assignments",
	"assignment_overrides",
	"checkins",
	"resource_issues",
	"banners",
	"banner_roles",
	"banner_event_types",
	"banner_events",
	"settings",
	"ranking_rules",
	"headcount_overrides",
	"signup_overrides",
}

// Archive is the on-disk envelope.
type Archive struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"format_version"`
	SchemaVersion string    `json:"schema_version"`
	ExportedAt    string    `json:"exported_at"`
	Tables        []Section `json:"tables"`
}

// Section holds every row of one table.
type Section struct {
	Name string           `json:"name"`
	Rows []map[string]any `json:"rows"`
}

// Export writes a complete archive of db to w, read inside a single
// transaction so the snapshot is consistent.
func Export(ctx context.Context, db *sql.DB, w io.Writer) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	version, err := store.SchemaVersion(ctx, tx)
	if err != nil {
		return fmt.Errorf("archive: schema version: %w", err)
	}
	a := Archive{
		Format:        Format,
		FormatVersion: FormatVersion,
		SchemaVersion: version,
		ExportedAt:    entity.FormatTime(time.Now()),
	}
	for _, t := range Tables {
		rows, err := dumpTable(ctx, tx, t)
		if err != nil {
			return err
		}
		a.Tables = append(a.Tables, Section{Name: t, Rows: rows})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(a)
}

func dumpTable(ctx context.Context, tx *sql.Tx, table string) ([]map[string]any, error) {
	// table comes from the fixed Tables list, never from input.
	rows, err := tx.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("archive: export %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case nil, int64, float64, string:
				row[c] = v
			case []byte:
				// The schema has no BLOB columns; a []byte here is TEXT
				// returned as bytes by the driver.
				row[c] = string(v)
			default:
				return nil, fmt.Errorf("archive: export %s.%s: unsupported type %T", table, c, v)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Summary reports what an import did.
type Summary struct {
	Rows map[string]int // rows inserted per table
}

// ErrSchemaMismatch means the archive was written by a different schema
// version than the database is at.
var ErrSchemaMismatch = errors.New("archive: schema version mismatch")

// RestoreOverwrite replaces the entire contents of db with the archive read
// from r, atomically: on any error nothing is changed.
//
// Callers own the safety rails the spec requires around this (typed
// confirmation and an automatic pre-wipe export); this function is the
// mechanism only.
func RestoreOverwrite(ctx context.Context, db *sql.DB, r io.Reader) (Summary, error) {
	a, err := decode(r)
	if err != nil {
		return Summary{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()

	current, err := store.SchemaVersion(ctx, tx)
	if err != nil {
		return Summary{}, err
	}
	if a.SchemaVersion != current {
		return Summary{}, fmt.Errorf("%w: archive %q, database %q", ErrSchemaMismatch, a.SchemaVersion, current)
	}

	// Self-referencing tables (roles, event_types, persons.supervisor_id)
	// make row order within a table matter; check keys at commit instead.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return Summary{}, err
	}
	for i := len(Tables) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, `DELETE FROM "`+Tables[i]+`"`); err != nil {
			return Summary{}, fmt.Errorf("archive: clear %s: %w", Tables[i], err)
		}
	}

	known := make(map[string]bool, len(Tables))
	for _, t := range Tables {
		known[t] = true
	}
	sum := Summary{Rows: map[string]int{}}
	for _, s := range a.Tables {
		if !known[s.Name] {
			return Summary{}, fmt.Errorf("archive: unknown table %q", s.Name)
		}
		cols, err := tableColumns(ctx, tx, s.Name)
		if err != nil {
			return Summary{}, err
		}
		for n, row := range s.Rows {
			if err := insertRow(ctx, tx, s.Name, cols, row); err != nil {
				return Summary{}, fmt.Errorf("archive: %s row %d: %w", s.Name, n, err)
			}
		}
		sum.Rows[s.Name] = len(s.Rows)
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, fmt.Errorf("archive: commit (foreign keys are checked here): %w", err)
	}
	return sum, nil
}

func decode(r io.Reader) (Archive, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var a Archive
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("archive: decode: %w", err)
	}
	if a.Format != Format {
		return a, fmt.Errorf("archive: not an LSM archive (format %q)", a.Format)
	}
	if a.FormatVersion != FormatVersion {
		return a, fmt.Errorf("archive: unsupported format version %d", a.FormatVersion)
	}
	return a, nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols[c] = true
	}
	return cols, rows.Err()
}

func insertRow(ctx context.Context, tx *sql.Tx, table string, cols map[string]bool, row map[string]any) error {
	names := make([]string, 0, len(row))
	args := make([]any, 0, len(row))
	for c, v := range row {
		// Column names are interpolated, so only names the table actually
		// has are accepted.
		if !cols[c] {
			return fmt.Errorf("unknown column %q", c)
		}
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				v = i
			} else if f, err := n.Float64(); err == nil {
				v = f
			} else {
				return fmt.Errorf("column %q: bad number %s", c, n)
			}
		}
		names = append(names, `"`+c+`"`)
		args = append(args, v)
	}
	q := `INSERT INTO "` + table + `" (` + strings.Join(names, ", ") +
		`) VALUES (` + strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `)`
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}
