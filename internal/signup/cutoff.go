package signup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/store"
)

// Crossing is a person whose side of the line differs between the current
// and proposed weights.
type Crossing struct {
	PersonID    entity.ID `json:"person_id"`
	Name        string    `json:"name"`
	Department  string    `json:"department"`
	RankBefore  int       `json:"rank_before"`
	RankAfter   int       `json:"rank_after"`
	TotalBefore float64   `json:"total_before"`
	TotalAfter  float64   `json:"total_after"`
}

// EventPreview is the effect of a weight change on one event.
type EventPreview struct {
	EventID entity.ID `json:"event_id"`
	Event   string    `json:"event"`
	Date    string    `json:"date"`
	// Entering crosses above the line under the proposed weights; Leaving
	// drops below it. Both directions, so the trade is visible.
	Entering []Crossing `json:"entering"`
	Leaving  []Crossing `json:"leaving"`
}

// Proposal is a candidate change to the ranking policy.
type Proposal struct {
	Weights Weights
	// Expectation, if set, replaces the availability expectation.
	Expectation *float64
}

// Preview ranks each event under the current policy and the proposal and
// reports who crosses the line in each direction. Nothing is stored.
func Preview(ctx context.Context, q store.DBTX, eventIDs []entity.ID, current Weights, proposed Proposal) ([]EventPreview, error) {
	if err := proposed.Weights.Validate(); err != nil {
		return nil, err
	}
	if e := proposed.Expectation; e != nil && (*e <= 0 || *e > 1) {
		return nil, invalidf("expectation %.0f%% must be above 0 and at most 100", *e*100)
	}
	var out []EventPreview
	for _, id := range eventIDs {
		d, err := loadEvent(ctx, q, id)
		if err != nil {
			return nil, err
		}
		before := d.rank(current)
		if proposed.Expectation != nil {
			d.expectation = *proposed.Expectation
		}
		after := d.rank(proposed.Weights)
		p := EventPreview{EventID: id, Event: d.event.Name, Date: entity.FormatDate(d.event.Date),
			Entering: []Crossing{}, Leaving: []Crossing{}}
		for i, dep := range before.Departments {
			was := map[entity.ID]Candidate{}
			for _, c := range dep.Candidates {
				was[c.PersonID] = c
			}
			for _, c := range after.Departments[i].Candidates {
				b := was[c.PersonID]
				if b.AboveLine == c.AboveLine {
					continue
				}
				x := Crossing{PersonID: c.PersonID, Name: c.Name, Department: dep.Department,
					RankBefore: b.Rank, RankAfter: c.Rank, TotalBefore: b.Total, TotalAfter: c.Total}
				if c.AboveLine {
					p.Entering = append(p.Entering, x)
				} else {
					p.Leaving = append(p.Leaving, x)
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// EventIDsInMonth lists a month's events in date order.
func EventIDsInMonth(ctx context.Context, q store.DBTX, month string) ([]entity.ID, error) {
	if _, err := ParseMonth(month); err != nil {
		return nil, err
	}
	events, err := shifts.EventsInMonth(ctx, q, month)
	if err != nil {
		return nil, err
	}
	ids := make([]entity.ID, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids, nil
}

// PersonalRanking is one person's view of where they stand for an event:
// the answer to "why am I not on Saturday".
type PersonalRanking struct {
	EventID    entity.ID   `json:"event_id"`
	Event      string      `json:"event"`
	Date       string      `json:"date"`
	SignedUp   bool        `json:"signed_up"`
	Department string      `json:"department,omitempty"`
	Required   Requirement `json:"required"`
	Signups    int         `json:"signups"`
	// Candidate is the person's own row, with every rule's contribution.
	Candidate *Candidate `json:"candidate,omitempty"`
	// LineTotal is the total of the last person above the line, when the
	// line falls inside the list; nil when everyone who signed up is above.
	LineTotal *float64 `json:"line_total"`
	// Provisional is true until the roster is committed.
	Provisional bool `json:"provisional"`
}

// RankFor returns one person's own ranking for an event under the stored
// weights. It reveals nobody else's scores or identity.
func RankFor(ctx context.Context, q store.DBTX, eventID, personID entity.ID) (PersonalRanking, error) {
	w, err := LoadWeights(ctx, q)
	if err != nil {
		return PersonalRanking{}, err
	}
	r, err := RankEvent(ctx, q, eventID, w)
	if err != nil {
		return PersonalRanking{}, err
	}
	var committed int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_staff WHERE event_id = ?`, eventID).Scan(&committed); err != nil {
		return PersonalRanking{}, err
	}
	out := PersonalRanking{EventID: r.EventID, Event: r.Event, Date: r.Date, Provisional: committed == 0}
	for _, dep := range r.Departments {
		for i, c := range dep.Candidates {
			if c.PersonID != personID {
				continue
			}
			out.SignedUp, out.Department, out.Required, out.Signups = true, dep.Department, dep.Required, dep.Signups
			out.Candidate = &dep.Candidates[i]
			if last := dep.AboveLine - 1; last >= 0 && dep.AboveLine < len(dep.Candidates) {
				t := dep.Candidates[last].Total
				out.LineTotal = &t
			}
			return out, nil
		}
	}
	return out, nil
}

// PullAboveLine records an admin override placing a person above the line
// for an event. The person must have signed up for it.
func PullAboveLine(ctx context.Context, q store.DBTX, eventID, personID, by entity.ID, note string, now time.Time) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM availability
		WHERE event_id = ? AND person_id = ? AND status IN ('all_shifts', 'window')`, eventID, personID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: that person has not signed up for this event", ErrNotFound)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO signup_overrides (id, event_id, person_id, created_at, created_by, note)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		entity.NewID(), eventID, personID, entity.FormatTime(now), by, note)
	return err
}

// WithdrawOverride removes a live override, keeping it in the history.
func WithdrawOverride(ctx context.Context, q store.DBTX, eventID, personID, by entity.ID, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE signup_overrides SET removed_at = ?, removed_by = ?
		WHERE event_id = ? AND person_id = ? AND removed_at IS NULL`,
		entity.FormatTime(now), by, eventID, personID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetHeadcount sets (or, with nil, clears) a department's required
// headcount for either an event type or a single event.
func SetHeadcount(ctx context.Context, q store.DBTX, deptID entity.ID, eventTypeID, eventID entity.NullID,
	headcount *int, by entity.ID, now time.Time) error {
	if eventTypeID.Valid == eventID.Valid {
		return invalidf("headcount is set for an event type or an event, not both")
	}
	if headcount != nil && *headcount < 0 {
		return invalidf("headcount cannot be negative")
	}
	var parent entity.NullID
	if err := q.QueryRowContext(ctx, `SELECT parent_id FROM roles WHERE id = ?`, deptID).Scan(&parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if parent.Valid {
		return invalidf("headcount is set per department (a top-level role)")
	}
	col, target := "event_type_id", eventTypeID
	if eventID.Valid {
		col, target = "event_id", eventID
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM headcount_overrides WHERE role_id = ? AND `+col+` = ?`,
		deptID, target); err != nil {
		return err
	}
	if headcount == nil {
		return nil
	}
	_, err := q.ExecContext(ctx, `INSERT INTO headcount_overrides
		(id, role_id, event_type_id, event_id, headcount, updated_at, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entity.NewID(), deptID, eventTypeID, eventID, *headcount, entity.FormatTime(now), by)
	return err
}

// RosterResult reports a committed roster.
type RosterResult struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Kept    int `json:"kept"`
}

// CommitRoster writes the current recommendation to the event's roster
// (event_staff), which the matcher consumes. It is idempotent: people who
// stay on keep their row (and any supervisor allocation), people who fell
// below the line are removed.
func CommitRoster(ctx context.Context, db *sql.DB, eventID entity.ID) (RosterResult, error) {
	var res RosterResult
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		w, err := LoadWeights(ctx, tx)
		if err != nil {
			return err
		}
		r, err := RankEvent(ctx, tx, eventID, w)
		if err != nil {
			return err
		}
		above := map[entity.ID]Candidate{}
		for _, dep := range r.Departments {
			for _, c := range dep.Candidates {
				if c.AboveLine {
					above[c.PersonID] = c
				}
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT person_id FROM event_staff WHERE event_id = ?`, eventID)
		if err != nil {
			return err
		}
		existing := map[entity.ID]bool{}
		for rows.Next() {
			var id entity.ID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for id := range existing {
			if _, ok := above[id]; ok {
				res.Kept++
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM event_staff WHERE event_id = ? AND person_id = ?`, eventID, id); err != nil {
				return err
			}
			res.Removed++
		}
		for id, c := range above {
			if existing[id] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO event_staff (event_id, person_id, signed_up_at) VALUES (?, ?, ?)`,
				eventID, id, entity.FormatTime(c.Signup.SignedUpAt)); err != nil {
				return err
			}
			res.Added++
		}
		return nil
	})
	return res, err
}
