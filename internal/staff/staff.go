package staff

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// Tier is a person's standing access level. Lead is deliberately not a
// tier: it is a property of the position a person holds for the night.
type Tier string

const (
	TierStaff      Tier = "staff"
	TierSupervisor Tier = "supervisor"
	TierAdmin      Tier = "admin"
)

// Valid reports whether t is a stored tier.
func (t Tier) Valid() bool {
	return t == TierStaff || t == TierSupervisor || t == TierAdmin
}

// Gender exists solely for the roam-team pairing rule and is admin-only.
// Unknown means "not recorded", not a third category: an unknown person is
// eligible for either half of a roam pair.
type Gender string

const (
	GenderMale    Gender = "male"
	GenderFemale  Gender = "female"
	GenderUnknown Gender = "unknown"
)

// Valid reports whether g is a stored gender value.
func (g Gender) Valid() bool {
	return g == GenderMale || g == GenderFemale || g == GenderUnknown
}

// Role is a node in the role hierarchy (Event Security, Concessions →
// outlet …). Anything set on a role applies to everything beneath it.
type Role struct {
	ID        entity.ID
	ParentID  entity.NullID
	Name      string
	SortOrder int
}

// Person is a member of staff. Domain values are never serialised to
// clients directly: gender is admin-only, so responses go through
// tier-aware view types.
type Person struct {
	ID           entity.ID
	Badge        string // 1D barcode payload; unique, but not the key
	Name         string
	Photo        string
	RoleID       entity.ID
	Tier         Tier
	HireDate     time.Time // tenure is computed from this, never stored
	Gender       Gender
	SupervisorID entity.NullID
	Email        string
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Tenure returns the person's general arena experience as of asOf.
func (p Person) Tenure(asOf time.Time) HalfYears { return Tenure(p.HireDate, asOf) }

// InsertRole stores a new role.
func InsertRole(ctx context.Context, q store.DBTX, r Role) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO roles (id, parent_id, name, sort_order) VALUES (?, ?, ?, ?)`,
		r.ID, r.ParentID, r.Name, r.SortOrder)
	if err != nil {
		return fmt.Errorf("staff: insert role %q: %w", r.Name, err)
	}
	return nil
}

// ListRoles returns every role, parents before children within each
// level's sort order.
func ListRoles(ctx context.Context, q store.DBTX) ([]Role, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, parent_id, name, sort_order FROM roles ORDER BY parent_id IS NOT NULL, sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.ParentID, &r.Name, &r.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const personColumns = `id, badge, name, COALESCE(photo, ''), role_id, tier, hire_date, gender,
	supervisor_id, COALESCE(email, ''), active, created_at, updated_at`

// InsertPerson stores a new person. CreatedAt and UpdatedAt default to now.
func InsertPerson(ctx context.Context, q store.DBTX, p Person) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	_, err := q.ExecContext(ctx, `INSERT INTO persons
		(id, badge, name, photo, role_id, tier, hire_date, gender, supervisor_id, email, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Badge, p.Name, nullString(p.Photo), p.RoleID, p.Tier, entity.FormatDate(p.HireDate),
		p.Gender, p.SupervisorID, nullString(p.Email), p.Active,
		entity.FormatTime(p.CreatedAt), entity.FormatTime(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("staff: insert person %s: %w", p.Badge, err)
	}
	return nil
}

// PersonByBadge resolves a scanned badge to a person. It returns
// sql.ErrNoRows when the badge is not recognised.
func PersonByBadge(ctx context.Context, q store.DBTX, badge string) (Person, error) {
	return scanPerson(q.QueryRowContext(ctx, `SELECT `+personColumns+` FROM persons WHERE badge = ?`, badge))
}

// PersonByID returns one person, or sql.ErrNoRows.
func PersonByID(ctx context.Context, q store.DBTX, id entity.ID) (Person, error) {
	return scanPerson(q.QueryRowContext(ctx, `SELECT `+personColumns+` FROM persons WHERE id = ?`, id))
}

// ListPersons returns every person ordered by name.
func ListPersons(ctx context.Context, q store.DBTX) ([]Person, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+personColumns+` FROM persons ORDER BY name, badge`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanPerson(s scanner) (Person, error) {
	var (
		p                 Person
		hire, created, up string
	)
	err := s.Scan(&p.ID, &p.Badge, &p.Name, &p.Photo, &p.RoleID, &p.Tier, &hire, &p.Gender,
		&p.SupervisorID, &p.Email, &p.Active, &created, &up)
	if err != nil {
		return Person{}, err
	}
	if p.HireDate, err = entity.ParseDate(hire); err != nil {
		return Person{}, err
	}
	if p.CreatedAt, err = entity.ParseTime(created); err != nil {
		return Person{}, err
	}
	if p.UpdatedAt, err = entity.ParseTime(up); err != nil {
		return Person{}, err
	}
	return p, nil
}

// Credential is a person's login. It lives off the person row so it never
// rides along in a profile query.
type Credential struct {
	PersonID     entity.ID
	Username     string
	PasswordHash string // "<scheme>:<hash>", e.g. the seed's "sha256:…"
	// MustChangePassword forces a reset at next sign-in.
	MustChangePassword bool
	UpdatedAt          time.Time
}

// InsertCredential stores a person's login.
func InsertCredential(ctx context.Context, q store.DBTX, c Credential) error {
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = time.Now()
	}
	_, err := q.ExecContext(ctx, `INSERT INTO credentials
		(person_id, username, password_hash, must_change_password, updated_at) VALUES (?, ?, ?, ?, ?)`,
		c.PersonID, c.Username, c.PasswordHash, c.MustChangePassword, entity.FormatTime(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("staff: insert credential %q: %w", c.Username, err)
	}
	return nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
