// Package store opens the database and applies schema migrations.
//
// SQLite (pure Go, no cgo) backs the demo. Domain packages depend on
// *sql.DB / *sql.Tx and portable SQL, not on this driver, so moving to
// Postgres later is a driver swap plus a migration dialect pass.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lsm/internal/entity"
	"lsm/migrations"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Open opens (creating if needed) the SQLite database at path with the
// connection settings LSM relies on: enforced foreign keys, WAL so the
// kiosk and reception can read while a write is in flight, and a busy
// timeout instead of immediate SQLITE_BUSY errors.
func Open(path string) (*sql.DB, error) {
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		path = abs
	}
	q := url.Values{}
	for _, p := range []string{
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
		"cache_size(-32000)", // 32 MB page cache: the demo database fits
	} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if path == ":memory:" {
		// Each connection to :memory: is a separate database.
		db.SetMaxOpenConns(1)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return db, nil
}

// Migration is one embedded schema file.
type Migration struct {
	Version  string // "0001"
	Name     string // "0001_core.sql"
	SQL      string
	Checksum string
}

// Migrations lists the embedded migrations in application order.
func Migrations() ([]Migration, error) {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Migration, 0, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			return nil, err
		}
		v, _, ok := strings.Cut(n, "_")
		if !ok || len(v) != 4 {
			return nil, fmt.Errorf("store: migration %s: name must be NNNN_description.sql", n)
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{Version: v, Name: n, SQL: string(b), Checksum: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// Migrate applies every pending migration, each in its own transaction,
// and returns the names applied. It refuses to run if an already-applied
// migration has been edited since: shipped migrations are immutable.
func Migrate(ctx context.Context, db *sql.DB) ([]string, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		checksum   TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("store: schema_migrations: %w", err)
	}

	applied := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	all, err := Migrations()
	if err != nil {
		return nil, err
	}
	var done []string
	for _, m := range all {
		if sum, ok := applied[m.Version]; ok {
			if sum != m.Checksum {
				return done, fmt.Errorf("store: migration %s was modified after being applied", m.Name)
			}
			continue
		}
		if err := apply(ctx, db, m); err != nil {
			return done, err
		}
		done = append(done, m.Name)
	}
	return done, nil
}

func apply(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("store: applying %s: %w", m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Checksum, entity.FormatTime(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

// DBTX is the query surface shared by *sql.DB and *sql.Tx. Domain
// functions take a DBTX so callers decide the transaction boundary.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// InTx runs fn inside a transaction, committing if it returns nil and
// rolling back otherwise.
func InTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// SchemaVersion returns the highest applied migration version, or "" if
// none has been applied.
func SchemaVersion(ctx context.Context, q DBTX) (string, error) {
	var v sql.NullString
	err := q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return v.String, err
}
