package signup

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/store"
)

// Selection is a person's current answer for one event.
type Selection struct {
	Status   shifts.AvailabilityStatus `json:"status"` // "" = not answered yet
	WindowID *entity.ID                `json:"window_id"`
}

// Available reports whether the answer offers the person for the event.
func (s Selection) Available() bool {
	return s.Status == shifts.AllShifts || s.Status == shifts.OneWindow
}

// WindowView is a shift window in the venue's local time.
type WindowView struct {
	ID    entity.ID `json:"id"`
	Start string    `json:"start"` // local HH:MM
	End   string    `json:"end"`
	Label string    `json:"label"` // "1:30–9:30 pm"
}

// EventCard is one open event as shown on the Availability tab.
type EventCard struct {
	ID        entity.ID    `json:"id"`
	Name      string       `json:"name"`
	Date      string       `json:"date"`
	Windows   []WindowView `json:"windows"`
	Selection Selection    `json:"selection"`
}

// Rate is a person's signup rate for a month.
type Rate struct {
	Available int     `json:"available"` // events offered for
	Events    int     `json:"events"`    // events in the month
	Percent   float64 `json:"percent"`   // 0..100, one decimal
}

func newRate(available, events int) Rate {
	r := Rate{Available: available, Events: events}
	if events > 0 {
		r.Percent = math.Round(1000*float64(available)/float64(events)) / 10
	}
	return r
}

// MonthView is the Availability tab for one person and month.
type MonthView struct {
	Month  string             `json:"month"`
	Status shifts.MonthStatus `json:"status"`
	// Expectation is the published policy as a percentage (60).
	Expectation float64     `json:"expectation_percent"`
	Rate        Rate        `json:"rate"`
	Editable    bool        `json:"editable"`
	Events      []EventCard `json:"events"`
}

// PersonMonth returns a person's availability for an opened month. An
// unopened month is ErrNotFound: staff never see it.
func PersonMonth(ctx context.Context, q store.DBTX, personID entity.ID, month string, loc *time.Location) (MonthView, error) {
	if _, err := ParseMonth(month); err != nil {
		return MonthView{}, err
	}
	status, err := MonthStatus(ctx, q, month)
	if err != nil {
		return MonthView{}, err
	}
	if status == "" {
		return MonthView{}, ErrNotFound
	}
	exp, err := Expectation(ctx, q)
	if err != nil {
		return MonthView{}, err
	}
	v := MonthView{Month: month, Status: status, Expectation: math.Round(exp * 100), Editable: status == shifts.MonthOpen}

	events, err := shifts.EventsInMonth(ctx, q, month)
	if err != nil {
		return MonthView{}, err
	}
	answers, err := personAnswers(ctx, q, personID, month)
	if err != nil {
		return MonthView{}, err
	}
	available := 0
	for _, e := range events {
		card, err := eventCard(ctx, q, e, answers[e.ID], loc)
		if err != nil {
			return MonthView{}, err
		}
		if card.Selection.Available() {
			available++
		}
		v.Events = append(v.Events, card)
	}
	v.Rate = newRate(available, len(events))
	return v, nil
}

func personAnswers(ctx context.Context, q store.DBTX, personID entity.ID, month string) (map[entity.ID]Selection, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.event_id, a.status, a.window_id FROM availability a
		JOIN events e ON e.id = a.event_id
		WHERE a.person_id = ? AND substr(e.event_date, 1, 7) = ?`, personID, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[entity.ID]Selection{}
	for rows.Next() {
		var (
			id  entity.ID
			s   Selection
			win entity.NullID
		)
		if err := rows.Scan(&id, &s.Status, &win); err != nil {
			return nil, err
		}
		if win.Valid {
			s.WindowID = &win.ID
		}
		out[id] = s
	}
	return out, rows.Err()
}

// EventWindows returns an event's shift windows in the venue's time.
func EventWindows(ctx context.Context, q store.DBTX, eventID entity.ID, loc *time.Location) ([]WindowView, error) {
	e, err := shifts.EventByID(ctx, q, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c, err := eventCard(ctx, q, e, Selection{}, loc)
	return c.Windows, err
}

func eventCard(ctx context.Context, q store.DBTX, e shifts.Event, sel Selection, loc *time.Location) (EventCard, error) {
	windows, err := shifts.ShiftWindows(ctx, q, e.ID)
	if err != nil {
		return EventCard{}, err
	}
	c := EventCard{ID: e.ID, Name: e.Name, Date: entity.FormatDate(e.Date), Selection: sel}
	for _, w := range windows {
		s, en := w.Start.In(loc), w.End.In(loc)
		c.Windows = append(c.Windows, WindowView{
			ID: w.ID, Start: s.Format("15:04"), End: en.Format("15:04"),
			Label: s.Format("3:04") + "–" + en.Format("3:04 pm"),
		})
	}
	return c, nil
}

// SetAvailability records a person's answer for an event in an open
// month, writing through immediately. The first time a person offers
// themselves is kept as their signup time (the ranking tiebreak): moving
// between windows keeps it, withdrawing and re-offering does not.
func SetAvailability(ctx context.Context, db *sql.DB, personID, eventID entity.ID,
	sel Selection, now time.Time, loc *time.Location) (EventCard, Rate, error) {
	if !sel.Status.Valid() {
		return EventCard{}, Rate{}, invalidf("invalid status %q", sel.Status)
	}
	if (sel.Status == shifts.OneWindow) != (sel.WindowID != nil) {
		return EventCard{}, Rate{}, invalidf("a window is chosen exactly when status is \"window\"")
	}
	var (
		card EventCard
		rate Rate
	)
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		e, err := shifts.EventByID(ctx, tx, eventID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		status, err := MonthStatus(ctx, tx, e.Month())
		if err != nil {
			return err
		}
		if status == "" {
			return ErrNotFound
		}
		if status != shifts.MonthOpen {
			return ErrMonthNotOpen
		}
		if sel.WindowID != nil {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shift_windows WHERE id = ? AND event_id = ?`,
				*sel.WindowID, eventID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return invalidf("window %s is not a shift window of this event", *sel.WindowID)
			}
		}

		var prev struct {
			status shifts.AvailabilityStatus
			signed string
		}
		err = tx.QueryRowContext(ctx, `SELECT status, signed_up_at FROM availability WHERE person_id = ? AND event_id = ?`,
			personID, eventID).Scan(&prev.status, &prev.signed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		signed := entity.FormatTime(now)
		if wasAvailable := prev.status == shifts.AllShifts || prev.status == shifts.OneWindow; wasAvailable && sel.Available() {
			signed = prev.signed
		}
		var win entity.NullID
		if sel.WindowID != nil {
			win = entity.Some(*sel.WindowID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO availability
			(id, person_id, event_id, window_id, status, signed_up_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (person_id, event_id) DO UPDATE SET
				window_id = excluded.window_id, status = excluded.status,
				signed_up_at = excluded.signed_up_at, updated_at = excluded.updated_at`,
			entity.NewID(), personID, eventID, win, sel.Status, signed, entity.FormatTime(now)); err != nil {
			return err
		}

		if card, err = eventCard(ctx, tx, e, sel, loc); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT
				COALESCE(SUM(a.status IN ('all_shifts', 'window')), 0), COUNT(*)
			FROM events e LEFT JOIN availability a ON a.event_id = e.id AND a.person_id = ?
			WHERE substr(e.event_date, 1, 7) = ?`, personID, e.Month()).Scan(&rate.Available, &rate.Events)
	})
	if err != nil {
		return EventCard{}, Rate{}, err
	}
	return card, newRate(rate.Available, rate.Events), nil
}
