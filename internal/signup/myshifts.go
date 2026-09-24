package signup

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"lsm/internal/banner"
	"lsm/internal/entity"
	"lsm/internal/staff"
	"lsm/internal/store"
)

// Shift is one event on a person's own schedule.
type Shift struct {
	EventID entity.ID `json:"event_id"`
	Event   string    `json:"event"`
	Date    string    `json:"date"`
	Start   string    `json:"start"` // local HH:MM
	End     string    `json:"end"`
	// TimeLabel is "2:00–9:30 pm": the window they signed up for, or the
	// whole event when they offered all shifts.
	TimeLabel string `json:"time_label"`
	// StartsAt and EndsAt are the same times as instants, for calendar
	// entries.
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	// Area is the assigned post's area once the matcher has run;
	// until then, the department.
	Area   string `json:"area"`
	Status string `json:"status"` // "rostered" (upcoming or past) or "worked"
	Notes  string `json:"notes"`
}

// PersonShifts returns a person's shifts with dates in [from, to]: every
// event they are on the committed roster for, plus past events they
// worked (checked in or were rated for).
func PersonShifts(ctx context.Context, q store.DBTX, personID entity.ID, from, to time.Time, loc *time.Location) ([]Shift, error) {
	p, err := staff.PersonByID(ctx, q, personID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	roles, err := staff.ListRoles(ctx, q)
	if err != nil {
		return nil, err
	}
	dept := Departments(roles)[p.RoleID].Name

	rows, err := q.QueryContext(ctx, `
		SELECT e.id, e.name, e.event_date, e.start_at, e.end_at, e.notes,
		       EXISTS (SELECT 1 FROM event_staff s WHERE s.event_id = e.id AND s.person_id = ?1) AS rostered,
		       COALESCE(a.status, ''), a.window_id,
		       COALESCE((SELECT pos.area FROM assignments asg JOIN positions pos ON pos.id = asg.position_id
		                 WHERE asg.event_id = e.id AND asg.person_id = ?1 AND asg.removed_at IS NULL
		                 ORDER BY asg.shift = 'first' DESC LIMIT 1), '')
		FROM events e
		LEFT JOIN availability a ON a.event_id = e.id AND a.person_id = ?1
		WHERE e.event_date BETWEEN ?2 AND ?3 AND (
		      EXISTS (SELECT 1 FROM event_staff s WHERE s.event_id = e.id AND s.person_id = ?1)
		   OR EXISTS (SELECT 1 FROM checkins c WHERE c.event_id = e.id AND c.person_id = ?1)
		   OR EXISTS (SELECT 1 FROM rating_entries r WHERE r.event_id = e.id AND r.person_id = ?1))
		ORDER BY e.start_at`, personID, entity.FormatDate(from), entity.FormatDate(to))
	if err != nil {
		return nil, err
	}
	type row struct {
		s          Shift
		start, end string
		rostered   bool
		status     string
		window     entity.NullID
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.s.EventID, &r.s.Event, &r.s.Date, &r.start, &r.end, &r.s.Notes,
			&r.rostered, &r.status, &r.window, &r.s.Area); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Shift, 0, len(found))
	for _, r := range found {
		s := r.s
		if s.StartsAt, err = entity.ParseTime(r.start); err != nil {
			return nil, err
		}
		if s.EndsAt, err = entity.ParseTime(r.end); err != nil {
			return nil, err
		}
		if r.window.Valid {
			var ws, we string
			err := q.QueryRowContext(ctx, `SELECT start_at, end_at FROM shift_windows WHERE id = ?`, r.window.ID).Scan(&ws, &we)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				s.StartsAt, _ = entity.ParseTime(ws)
				s.EndsAt, _ = entity.ParseTime(we)
			}
		}
		st, en := s.StartsAt.In(loc), s.EndsAt.In(loc)
		s.Start, s.End = st.Format("15:04"), en.Format("15:04")
		s.TimeLabel = st.Format("3:04") + "–" + en.Format("3:04 pm")
		if s.Area == "" {
			s.Area = dept
		}
		s.Status = "rostered"
		if !r.rostered {
			s.Status = "worked"
		}
		out = append(out, s)
	}
	return out, nil
}

// Home is the app's Home tab.
type Home struct {
	Name     string         `json:"name"`
	Banner   *banner.Banner `json:"banner"`
	Upcoming []Shift        `json:"upcoming"`
}

// HomeFor builds the Home tab: the banner for the person's next shift (or,
// with none rostered, the next event in an open month) and their next few
// shifts.
func HomeFor(ctx context.Context, q store.DBTX, personID entity.ID, now time.Time, loc *time.Location) (Home, error) {
	p, err := staff.PersonByID(ctx, q, personID)
	if err != nil {
		return Home{}, err
	}
	today := now.In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	shifts, err := PersonShifts(ctx, q, personID, today, today.AddDate(1, 0, 0), loc)
	if err != nil {
		return Home{}, err
	}
	h := Home{Name: p.Name, Upcoming: []Shift{}}
	for _, s := range shifts {
		if s.Status == "rostered" && len(h.Upcoming) < 5 {
			h.Upcoming = append(h.Upcoming, s)
		}
	}

	var next entity.ID
	if len(h.Upcoming) > 0 {
		next = h.Upcoming[0].EventID
	} else {
		err := q.QueryRowContext(ctx, `SELECT e.id FROM events e
			JOIN schedule_months sm ON sm.month = substr(e.event_date, 1, 7) AND sm.status = 'open'
			WHERE e.event_date >= ? ORDER BY e.start_at LIMIT 1`, entity.FormatDate(today)).Scan(&next)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Home{}, err
		}
	}
	if !next.IsNil() {
		if h.Banner, err = banner.For(ctx, q, p.RoleID, next); err != nil {
			return Home{}, err
		}
	}
	return h, nil
}
