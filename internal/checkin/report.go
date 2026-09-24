package checkin

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/store"
)

// Outstanding is one issued item not yet returned.
type Outstanding struct {
	IssueID    entity.ID `json:"issue_id"`
	EventID    entity.ID `json:"event_id"`
	Event      string    `json:"event,omitempty"` // set for items from earlier nights
	EventDate  string    `json:"event_date"`
	ResourceID entity.ID `json:"resource_id"`
	Resource   string    `json:"resource"`
	Number     string    `json:"number,omitempty"`
	Registered bool      `json:"registered"`
	PersonID   entity.ID `json:"person_id"`
	Name       string    `json:"name"`
	Badge      string    `json:"badge"`
	Post       string    `json:"post,omitempty"`
	Issued     string    `json:"issued"`
	IssuedBy   string    `json:"issued_by,omitempty"`
	// CheckedOut is set when the person left without it being returned:
	// the case the report exists to catch.
	CheckedOut string `json:"checked_out,omitempty"`
}

// Slot is one registered number on the rack.
type Slot struct {
	Number string `json:"number"`
	// State is "in" (on the rack), "out" (out tonight), "earlier" (out
	// since an earlier night) or "retired".
	State  string       `json:"state"`
	Holder *Outstanding `json:"holder,omitempty"`
}

// ResourceReport is one resource's reconciliation for the night.
type ResourceReport struct {
	ResourceID entity.ID `json:"resource_id"`
	Resource   string    `json:"resource"`
	Tracked    bool      `json:"tracked"`
	Issued     int       `json:"issued"`   // issued tonight
	Returned   int       `json:"returned"` // of those, returned
	// Rack is every registered number, in rack order (tracked only).
	Rack []Slot `json:"rack,omitempty"`
	// Outstanding lists tonight's unreturned items; for tracked resources
	// it includes free-typed numbers that have no rack slot.
	Outstanding []Outstanding `json:"outstanding"`
}

// UnreturnedReport is the end-of-night reconciliation (SPEC §3 Reports):
// what went out tonight and has not come back, laid out the way the radio
// rack is checked — by number, gaps named.
type UnreturnedReport struct {
	EventID   entity.ID        `json:"event_id"`
	Event     string           `json:"event"`
	Date      string           `json:"date"`
	Generated string           `json:"generated"`
	Issued    int              `json:"issued"`
	Returned  int              `json:"returned"`
	Out       int              `json:"out"`
	Resources []ResourceReport `json:"resources"`
	// Earlier lists items still out from previous nights: not tonight's
	// problem, but still missing from the rack.
	Earlier []Outstanding `json:"earlier"`
}

// Unreturned builds the report for one event.
func Unreturned(ctx context.Context, q store.DBTX, eventID entity.ID, now time.Time, loc *time.Location) (UnreturnedReport, error) {
	e, err := shifts.EventByID(ctx, q, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return UnreturnedReport{}, ErrNotFound
	}
	if err != nil {
		return UnreturnedReport{}, err
	}
	r := UnreturnedReport{EventID: e.ID, Event: e.Name, Date: entity.FormatDate(e.Date),
		Generated: now.In(loc).Format("Mon 2 Jan 3:04 pm"), Resources: []ResourceReport{}, Earlier: []Outstanding{}}

	// Every item still out, on any night, with who holds it.
	rows, err := q.QueryContext(ctx, `SELECT i.id, c.event_id, ev.name, ev.event_date, r.id, r.name,
			COALESCE(i.instance_number, ''), i.instance_id IS NOT NULL,
			p.id, p.name, p.badge, i.issued_at, COALESCE(b.name, ''), c.checked_out_at,
			COALESCE((SELECT pos.name FROM assignments a JOIN positions pos ON pos.id = a.position_id
			          WHERE a.event_id = c.event_id AND a.person_id = p.id AND a.removed_at IS NULL
			          ORDER BY a.shift = 'first' DESC LIMIT 1), '')
		FROM resource_issues i
		JOIN checkins c ON c.id = i.checkin_id
		JOIN events ev ON ev.id = c.event_id
		JOIN resources r ON r.id = i.resource_id
		JOIN persons p ON p.id = c.person_id
		LEFT JOIN persons b ON b.id = i.issued_by
		WHERE i.returned_at IS NULL AND ev.event_date <= ?
		ORDER BY ev.event_date, r.name, i.issued_at`, entity.FormatDate(e.Date))
	if err != nil {
		return UnreturnedReport{}, err
	}
	var all []Outstanding
	for rows.Next() {
		var (
			o       Outstanding
			issued  string
			outAt   sql.NullString
			eventNm string
		)
		if err := rows.Scan(&o.IssueID, &o.EventID, &eventNm, &o.EventDate, &o.ResourceID, &o.Resource, &o.Number,
			&o.Registered, &o.PersonID, &o.Name, &o.Badge, &issued, &o.IssuedBy, &outAt, &o.Post); err != nil {
			rows.Close()
			return UnreturnedReport{}, err
		}
		t, _ := entity.ParseTime(issued)
		o.Issued = clock(t, loc)
		if outAt.Valid {
			t, _ := entity.ParseTime(outAt.String)
			o.CheckedOut = clock(t, loc)
		}
		if o.EventID != e.ID {
			o.Event = eventNm
		}
		all = append(all, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return UnreturnedReport{}, err
	}

	resources, err := ListResources(ctx, q)
	if err != nil {
		return UnreturnedReport{}, err
	}
	for _, res := range resources {
		rr := ResourceReport{ResourceID: res.ID, Resource: res.Name, Tracked: res.Tracked, Outstanding: []Outstanding{}}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(i.returned_at IS NOT NULL), 0)
			FROM resource_issues i JOIN checkins c ON c.id = i.checkin_id
			WHERE c.event_id = ? AND i.resource_id = ?`, e.ID, res.ID).Scan(&rr.Issued, &rr.Returned); err != nil {
			return UnreturnedReport{}, err
		}
		holders := map[string]*Outstanding{}
		for i := range all {
			o := &all[i]
			if o.ResourceID != res.ID {
				continue
			}
			if o.EventID == e.ID {
				rr.Outstanding = append(rr.Outstanding, *o)
			}
			if o.Number != "" {
				// Tonight's holder wins over a stale earlier record.
				if prev, ok := holders[o.Number]; !ok || prev.EventID != e.ID {
					holders[o.Number] = o
				}
			}
		}
		sortByNumber(rr.Outstanding)

		if res.Tracked {
			instances, err := ResourceInstances(ctx, q, res.ID)
			if err != nil {
				return UnreturnedReport{}, err
			}
			for _, in := range instances {
				s := Slot{Number: in.Number, State: "in"}
				switch h := holders[in.Number]; {
				case in.Retired:
					s.State = "retired"
				case h != nil && h.EventID == e.ID:
					s.State, s.Holder = "out", h
				case h != nil:
					s.State, s.Holder = "earlier", h
				}
				rr.Rack = append(rr.Rack, s)
			}
			sort.SliceStable(rr.Rack, func(i, j int) bool { return numberLess(rr.Rack[i].Number, rr.Rack[j].Number) })
		}
		if rr.Issued == 0 && len(rr.Outstanding) == 0 && !res.Tracked {
			continue // nothing to reconcile
		}
		r.Issued += rr.Issued
		r.Returned += rr.Returned
		r.Out += len(rr.Outstanding)
		r.Resources = append(r.Resources, rr)
	}
	for _, o := range all {
		if o.EventID != e.ID {
			r.Earlier = append(r.Earlier, o)
		}
	}
	return r, nil
}

func sortByNumber(xs []Outstanding) {
	sort.SliceStable(xs, func(i, j int) bool { return numberLess(xs[i].Number, xs[j].Number) })
}

// numberLess orders rack numbers numerically where they are numbers, so
// radio 9 comes before radio 10.
func numberLess(a, b string) bool {
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return x < y
	case errA == nil:
		return true
	case errB == nil:
		return false
	}
	return a < b
}
