package shifts

import (
	"context"
	"database/sql"
	"fmt"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// ShiftPattern is when in the night a post is staffed.
type ShiftPattern string

const (
	FirstShiftOnly  ShiftPattern = "first_shift_only"
	SecondShiftOnly ShiftPattern = "second_shift_only"
	FullSession     ShiftPattern = "full_session"
	// TwoShiftDoor is a door post whose holder redeploys at second shift.
	TwoShiftDoor ShiftPattern = "two_shift_door"
)

// Valid reports whether p is a stored pattern.
func (p ShiftPattern) Valid() bool {
	switch p {
	case FirstShiftOnly, SecondShiftOnly, FullSession, TwoShiftDoor:
		return true
	}
	return false
}

// PairingMixedGender is the roam-team rule: one male and one female. It is
// the only pairing rule, and roam teams are the only positions that set it.
const PairingMixedGender = "mixed_gender"

// Position is one post. Each door slot is its own position; there are no
// slots within positions.
type Position struct {
	ID            entity.ID
	Name          string
	DepartmentID  entity.ID // a role
	Area          string    // print grouping: lane or area
	Location      string
	Floor         string
	ShiftPattern  ShiftPattern
	NeedsBreaking bool
	IsLead        bool
	Desirability  int // admin-reorderable rank; lower = more desirable
	// Headcount is nominal, never a cap: over-assignment is always legal.
	Headcount   int
	PairingRule string // "" or PairingMixedGender
	// MinTenure is a general-experience floor in half-years; nil = none.
	// The matcher filters on it; a supervisor or admin may override.
	MinTenure *int
	Notes     string
}

// InsertPosition stores a new position. Its capabilities, event types,
// resources and lead pool are added separately.
func InsertPosition(ctx context.Context, q store.DBTX, p Position) error {
	if !p.ShiftPattern.Valid() {
		return fmt.Errorf("shifts: position %q: invalid shift pattern %q", p.Name, p.ShiftPattern)
	}
	var minTenure sql.NullInt64
	if p.MinTenure != nil {
		minTenure = sql.NullInt64{Int64: int64(*p.MinTenure), Valid: true}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO positions
		(id, name, department_id, area, location, floor, shift_pattern, needs_breaking, is_lead,
		 desirability, headcount, pairing_rule, min_tenure_half_years, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.DepartmentID, p.Area, p.Location, p.Floor, p.ShiftPattern, p.NeedsBreaking,
		p.IsLead, p.Desirability, p.Headcount,
		sql.NullString{String: p.PairingRule, Valid: p.PairingRule != ""}, minTenure, p.Notes)
	if err != nil {
		return fmt.Errorf("shifts: insert position %q: %w", p.Name, err)
	}
	return nil
}

// ListPositions returns every position in sheet order: area, then name.
func ListPositions(ctx context.Context, q store.DBTX) ([]Position, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, department_id, area, location, floor,
		shift_pattern, needs_breaking, is_lead, desirability, headcount,
		COALESCE(pairing_rule, ''), min_tenure_half_years, notes
		FROM positions ORDER BY area, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Position
	for rows.Next() {
		var (
			p         Position
			minTenure sql.NullInt64
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.DepartmentID, &p.Area, &p.Location, &p.Floor,
			&p.ShiftPattern, &p.NeedsBreaking, &p.IsLead, &p.Desirability, &p.Headcount,
			&p.PairingRule, &minTenure, &p.Notes); err != nil {
			return nil, err
		}
		if minTenure.Valid {
			v := int(minTenure.Int64)
			p.MinTenure = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// The reciprocal many-to-many mappings below are one row per pair, edited
// from either end. Adding an existing pair is a no-op.

// AddPositionCapability links a position to a capability it draws on.
func AddPositionCapability(ctx context.Context, q store.DBTX, positionID, capabilityID entity.ID) error {
	_, err := q.ExecContext(ctx, `INSERT INTO position_capabilities (position_id, capability_id)
		VALUES (?, ?) ON CONFLICT DO NOTHING`, positionID, capabilityID)
	return err
}

// AddPositionEventType makes a position apply to an event type and its
// subtypes.
func AddPositionEventType(ctx context.Context, q store.DBTX, positionID, eventTypeID entity.ID) error {
	_, err := q.ExecContext(ctx, `INSERT INTO position_event_types (position_id, event_type_id)
		VALUES (?, ?) ON CONFLICT DO NOTHING`, positionID, eventTypeID)
	return err
}

// AddPositionResource declares a resource the post needs issued.
func AddPositionResource(ctx context.Context, q store.DBTX, positionID, resourceID entity.ID, quantity int) error {
	_, err := q.ExecContext(ctx, `INSERT INTO position_resources (position_id, resource_id, quantity)
		VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, positionID, resourceID, quantity)
	return err
}

// AddLeadPoolMember puts a position under a lead position.
func AddLeadPoolMember(ctx context.Context, q store.DBTX, leadID, positionID entity.ID) error {
	_, err := q.ExecContext(ctx, `INSERT INTO position_lead_pool (lead_position_id, position_id)
		VALUES (?, ?) ON CONFLICT DO NOTHING`, leadID, positionID)
	return err
}
