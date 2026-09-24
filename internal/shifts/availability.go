package shifts

import (
	"context"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// AvailabilityStatus is what a person offered for an event.
type AvailabilityStatus string

const (
	NotAvailable AvailabilityStatus = "not_available"
	AllShifts    AvailabilityStatus = "all_shifts"
	// OneWindow means a single named shift window; WindowID says which.
	OneWindow AvailabilityStatus = "window"
)

// Valid reports whether s is a stored status.
func (s AvailabilityStatus) Valid() bool {
	return s == NotAvailable || s == AllShifts || s == OneWindow
}

// Availability is a person's signup for one event, before capping.
type Availability struct {
	ID       entity.ID
	PersonID entity.ID
	EventID  entity.ID
	WindowID entity.NullID // set only when Status is OneWindow
	Status   AvailabilityStatus
	// SignedUpAt is the first signup: the ranking's tiebreak.
	SignedUpAt time.Time
	UpdatedAt  time.Time
}

// InsertAvailability stores a new signup. The schema rejects a window that
// belongs to a different event.
func InsertAvailability(ctx context.Context, q store.DBTX, a Availability) error {
	if !a.Status.Valid() {
		return fmt.Errorf("shifts: invalid availability status %q", a.Status)
	}
	if (a.Status == OneWindow) != a.WindowID.Valid {
		return fmt.Errorf("shifts: availability status %q with window %v", a.Status, a.WindowID.Valid)
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = a.SignedUpAt
	}
	_, err := q.ExecContext(ctx, `INSERT INTO availability
		(id, person_id, event_id, window_id, status, signed_up_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.PersonID, a.EventID, a.WindowID, a.Status,
		entity.FormatTime(a.SignedUpAt), entity.FormatTime(a.UpdatedAt))
	if err != nil {
		return fmt.Errorf("shifts: insert availability: %w", err)
	}
	return nil
}

// EventAvailability returns every signup for an event in signup order.
func EventAvailability(ctx context.Context, q store.DBTX, eventID entity.ID) ([]Availability, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, person_id, event_id, window_id, status, signed_up_at, updated_at
		FROM availability WHERE event_id = ? ORDER BY signed_up_at, id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Availability
	for rows.Next() {
		var (
			a          Availability
			signed, up string
		)
		if err := rows.Scan(&a.ID, &a.PersonID, &a.EventID, &a.WindowID, &a.Status, &signed, &up); err != nil {
			return nil, err
		}
		if a.SignedUpAt, err = entity.ParseTime(signed); err != nil {
			return nil, err
		}
		if a.UpdatedAt, err = entity.ParseTime(up); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
