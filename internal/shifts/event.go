package shifts

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// EventType is a node in the event-type hierarchy (Hockey, Concert,
// College Sport → Basketball …).
type EventType struct {
	ID        entity.ID
	ParentID  entity.NullID
	Name      string
	SortOrder int
}

// InsertEventType stores a new event type.
func InsertEventType(ctx context.Context, q store.DBTX, t EventType) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO event_types (id, parent_id, name, sort_order) VALUES (?, ?, ?, ?)`,
		t.ID, t.ParentID, t.Name, t.SortOrder)
	if err != nil {
		return fmt.Errorf("shifts: insert event type %q: %w", t.Name, err)
	}
	return nil
}

// ListEventTypes returns every event type, top level first.
func ListEventTypes(ctx context.Context, q store.DBTX) ([]EventType, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, parent_id, name, sort_order FROM event_types
		ORDER BY parent_id IS NOT NULL, sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventType
	for rows.Next() {
		var t EventType
		if err := rows.Scan(&t.ID, &t.ParentID, &t.Name, &t.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Event is one night at the arena.
type Event struct {
	ID          entity.ID
	EventTypeID entity.ID
	Name        string
	Date        time.Time // local calendar date; its month is the schedule month
	Start       time.Time
	End         time.Time
	// SecondShift is when second shift begins; zero until known.
	SecondShift time.Time
	Notes       string
	// Published is when the assignment sheet was published; zero = draft.
	Published time.Time
}

// Month returns the schedule month the event belongs to, as YYYY-MM.
func (e Event) Month() string { return e.Date.Format("2006-01") }

const eventColumns = `id, event_type_id, name, event_date, start_at, end_at,
	COALESCE(second_shift_at, ''), notes, COALESCE(published_at, '')`

// InsertEvent stores a new event.
func InsertEvent(ctx context.Context, q store.DBTX, e Event) error {
	_, err := q.ExecContext(ctx, `INSERT INTO events
		(id, event_type_id, name, event_date, start_at, end_at, second_shift_at, notes, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.EventTypeID, e.Name, entity.FormatDate(e.Date),
		entity.FormatTime(e.Start), entity.FormatTime(e.End),
		nullTime(e.SecondShift), e.Notes, nullTime(e.Published))
	if err != nil {
		return fmt.Errorf("shifts: insert event %q: %w", e.Name, err)
	}
	return nil
}

// EventByID returns one event, or sql.ErrNoRows.
func EventByID(ctx context.Context, q store.DBTX, id entity.ID) (Event, error) {
	return scanEvent(q.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM events WHERE id = ?`, id))
}

// ListEvents returns every event in date order.
func ListEvents(ctx context.Context, q store.DBTX) ([]Event, error) {
	return queryEvents(ctx, q, `SELECT `+eventColumns+` FROM events ORDER BY start_at`)
}

// EventsInMonth returns the events of one schedule month (YYYY-MM).
func EventsInMonth(ctx context.Context, q store.DBTX, month string) ([]Event, error) {
	return queryEvents(ctx, q, `SELECT `+eventColumns+` FROM events
		WHERE substr(event_date, 1, 7) = ? ORDER BY start_at`, month)
}

func queryEvents(ctx context.Context, q store.DBTX, query string, args ...any) ([]Event, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanEvent(s scanner) (Event, error) {
	var (
		e                               Event
		date, start, end, second, pubAt string
	)
	if err := s.Scan(&e.ID, &e.EventTypeID, &e.Name, &date, &start, &end, &second, &e.Notes, &pubAt); err != nil {
		return Event{}, err
	}
	var err error
	if e.Date, err = entity.ParseDate(date); err != nil {
		return Event{}, err
	}
	if e.Start, err = entity.ParseTime(start); err != nil {
		return Event{}, err
	}
	if e.End, err = entity.ParseTime(end); err != nil {
		return Event{}, err
	}
	if e.SecondShift, err = parseOptionalTime(second); err != nil {
		return Event{}, err
	}
	if e.Published, err = parseOptionalTime(pubAt); err != nil {
		return Event{}, err
	}
	return e, nil
}

// ShiftWindow is one of an event's offered shifts (e.g. 2:00–9:30). A
// signup is for one window or for all of them.
type ShiftWindow struct {
	ID        entity.ID
	EventID   entity.ID
	Start     time.Time
	End       time.Time
	SortOrder int
}

// InsertShiftWindow stores a new shift window.
func InsertShiftWindow(ctx context.Context, q store.DBTX, w ShiftWindow) error {
	_, err := q.ExecContext(ctx, `INSERT INTO shift_windows (id, event_id, start_at, end_at, sort_order)
		VALUES (?, ?, ?, ?, ?)`,
		w.ID, w.EventID, entity.FormatTime(w.Start), entity.FormatTime(w.End), w.SortOrder)
	if err != nil {
		return fmt.Errorf("shifts: insert shift window %s: %w", w.ID, err)
	}
	return nil
}

// ShiftWindows returns an event's windows in display order.
func ShiftWindows(ctx context.Context, q store.DBTX, eventID entity.ID) ([]ShiftWindow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, event_id, start_at, end_at, sort_order
		FROM shift_windows WHERE event_id = ? ORDER BY sort_order, start_at`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShiftWindow
	for rows.Next() {
		var (
			w          ShiftWindow
			start, end string
		)
		if err := rows.Scan(&w.ID, &w.EventID, &start, &end, &w.SortOrder); err != nil {
			return nil, err
		}
		if w.Start, err = entity.ParseTime(start); err != nil {
			return nil, err
		}
		if w.End, err = entity.ParseTime(end); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MonthStatus is whether a schedule month is taking signups.
type MonthStatus string

const (
	MonthOpen   MonthStatus = "open"
	MonthClosed MonthStatus = "closed"
)

// ScheduleMonth is a month an admin has opened (or since closed). A month
// with no record has not been opened.
type ScheduleMonth struct {
	ID        entity.ID
	Month     string // YYYY-MM
	Status    MonthStatus
	ChangedAt time.Time
	ChangedBy entity.NullID
}

// InsertScheduleMonth stores a month's status.
func InsertScheduleMonth(ctx context.Context, q store.DBTX, m ScheduleMonth) error {
	_, err := q.ExecContext(ctx, `INSERT INTO schedule_months (id, month, status, changed_at, changed_by)
		VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.Month, m.Status, entity.FormatTime(m.ChangedAt), m.ChangedBy)
	if err != nil {
		return fmt.Errorf("shifts: insert schedule month %s: %w", m.Month, err)
	}
	return nil
}

// ListScheduleMonths returns every opened or closed month in order.
func ListScheduleMonths(ctx context.Context, q store.DBTX) ([]ScheduleMonth, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, month, status, changed_at, changed_by FROM schedule_months ORDER BY month`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleMonth
	for rows.Next() {
		var (
			m  ScheduleMonth
			at string
		)
		if err := rows.Scan(&m.ID, &m.Month, &m.Status, &at, &m.ChangedBy); err != nil {
			return nil, err
		}
		if m.ChangedAt, err = entity.ParseTime(at); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: entity.FormatTime(t), Valid: true}
}

func parseOptionalTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return entity.ParseTime(s)
}
