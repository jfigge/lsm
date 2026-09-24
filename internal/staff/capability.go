package staff

import (
	"context"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// Capability is a kind of post a person can express willingness for
// (doors, x-ray, landing …).
type Capability struct {
	ID   entity.ID
	Name string
	// StaffSelectable false = hidden preference: recorded, and visible to
	// supervisors and admins, but never offered on the staff form.
	StaffSelectable bool
	SortOrder       int
}

// CapabilityState is a person's willingness for one capability. The word
// is "restricted", never "barred".
type CapabilityState string

const (
	StateYes          CapabilityState = "yes"
	StateNoPreference CapabilityState = "no_preference"
	StateRestricted   CapabilityState = "restricted"
)

// Valid reports whether s is a stored state.
func (s CapabilityState) Valid() bool {
	return s == StateYes || s == StateNoPreference || s == StateRestricted
}

// InsertCapability stores a new capability.
func InsertCapability(ctx context.Context, q store.DBTX, c Capability) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO capabilities (id, name, staff_selectable, sort_order) VALUES (?, ?, ?, ?)`,
		c.ID, c.Name, c.StaffSelectable, c.SortOrder)
	if err != nil {
		return fmt.Errorf("staff: insert capability %q: %w", c.Name, err)
	}
	return nil
}

// ListCapabilities returns every capability in display order.
func ListCapabilities(ctx context.Context, q store.DBTX) ([]Capability, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, name, staff_selectable, sort_order FROM capabilities ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Capability
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ID, &c.Name, &c.StaffSelectable, &c.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddCapabilityRole offers a capability on a role's preference form. It is
// a no-op if the pair already exists.
func AddCapabilityRole(ctx context.Context, q store.DBTX, capabilityID, roleID entity.ID) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO capability_roles (capability_id, role_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		capabilityID, roleID)
	return err
}

// SetPersonCapability records a person's explicit willingness for a
// capability, replacing any earlier value. by is who made the change.
func SetPersonCapability(ctx context.Context, q store.DBTX, personID, capabilityID entity.ID,
	state CapabilityState, by entity.NullID, at time.Time) error {
	if !state.Valid() {
		return fmt.Errorf("staff: invalid capability state %q", state)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO person_capabilities
		(person_id, capability_id, state, updated_at, updated_by) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (person_id, capability_id) DO UPDATE SET
			state = excluded.state, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		personID, capabilityID, state, entity.FormatTime(at), by)
	return err
}
