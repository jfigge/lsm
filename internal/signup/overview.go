package signup

import (
	"context"
	"database/sql"
	"errors"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

// Department is a top-level role: the unit signups are capped within.
type Department struct {
	ID   entity.ID `json:"id"`
	Name string    `json:"name"`
}

// ListDepartments returns the top-level roles in display order.
func ListDepartments(ctx context.Context, q store.DBTX) ([]Department, error) {
	roles, err := staff.ListRoles(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []Department
	for _, r := range roles {
		if !r.ParentID.Valid {
			out = append(out, Department{ID: r.ID, Name: r.Name})
		}
	}
	return out, nil
}

// DepartmentSummary is one department's line for an event, without the
// ranking itself.
type DepartmentSummary struct {
	DepartmentID entity.ID   `json:"department_id"`
	Department   string      `json:"department"`
	Required     Requirement `json:"required"`
	Signups      int         `json:"signups"`
	Overrides    int         `json:"overrides"`
	AboveLine    int         `json:"above_line"`
	BelowLine    int         `json:"below_line"`
	Shortfall    int         `json:"shortfall"`
}

// EventSummary is one event in a month overview.
type EventSummary struct {
	ID          entity.ID           `json:"id"`
	Name        string              `json:"name"`
	Date        string              `json:"date"`
	EventType   string              `json:"event_type"`
	RosterSize  int                 `json:"roster_size"`
	Departments []DepartmentSummary `json:"departments"`
}

// MonthOverview summarises every event in a month: signups against the
// requirement per department. The line counts need no ranking — overrides
// take their slots and the ranking fills the rest — so this stays cheap
// enough to list a whole month.
func MonthOverview(ctx context.Context, q store.DBTX, month string) ([]EventSummary, error) {
	if _, err := ParseMonth(month); err != nil {
		return nil, err
	}
	events, err := shifts.EventsInMonth(ctx, q, month)
	if err != nil {
		return nil, err
	}
	roles, err := staff.ListRoles(ctx, q)
	if err != nil {
		return nil, err
	}
	deptOf := Departments(roles)
	out := []EventSummary{}
	for _, e := range events {
		types, err := eventTypeChain(ctx, q, e.EventTypeID)
		if err != nil {
			return nil, err
		}
		s := EventSummary{ID: e.ID, Name: e.Name, Date: entity.FormatDate(e.Date)}
		if len(types) > 0 {
			s.EventType = types[0].Name
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_staff WHERE event_id = ?`, e.ID).Scan(&s.RosterSize); err != nil {
			return nil, err
		}

		signups, overrides := map[entity.ID]int{}, map[entity.ID]int{}
		rows, err := q.QueryContext(ctx, `SELECT p.role_id, o.id IS NOT NULL FROM availability a
			JOIN persons p ON p.id = a.person_id
			LEFT JOIN signup_overrides o ON o.event_id = a.event_id AND o.person_id = a.person_id AND o.removed_at IS NULL
			WHERE a.event_id = ? AND a.status IN ('all_shifts', 'window') AND p.active = 1`, e.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				role       entity.ID
				overridden bool
			)
			if err := rows.Scan(&role, &overridden); err != nil {
				rows.Close()
				return nil, err
			}
			d := deptOf[role].ID
			signups[d]++
			if overridden {
				overrides[d]++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}

		for _, r := range roles {
			if r.ParentID.Valid {
				continue
			}
			req, _, _, err := requirement(ctx, q, r.ID, entity.Some(e.ID), types)
			if err != nil {
				return nil, err
			}
			n := signups[r.ID]
			if n == 0 && (req.Headcount == nil || *req.Headcount == 0) {
				continue
			}
			ds := DepartmentSummary{DepartmentID: r.ID, Department: r.Name, Required: req,
				Signups: n, Overrides: overrides[r.ID]}
			slots := n
			if req.Headcount != nil {
				slots = *req.Headcount
			}
			ds.AboveLine = ds.Overrides + min(max(slots-ds.Overrides, 0), n-ds.Overrides)
			ds.BelowLine = n - ds.AboveLine
			if req.Headcount != nil {
				ds.Shortfall = max(*req.Headcount-ds.AboveLine, 0)
			}
			s.Departments = append(s.Departments, ds)
		}
		out = append(out, s)
	}
	return out, nil
}

// TypeHeadcount is one event type's requirement per department, as the
// default every event of the type inherits.
type TypeHeadcount struct {
	EventTypeID entity.ID         `json:"event_type_id"`
	Name        string            `json:"name"`
	Depth       int               `json:"depth"` // 0 = top level
	Departments []TypeRequirement `json:"departments"`
}

// TypeRequirement is a department's requirement for an event type.
type TypeRequirement struct {
	DepartmentID entity.ID   `json:"department_id"`
	Department   string      `json:"department"`
	Required     Requirement `json:"required"`
	// Override is the value set on this very type, if any; a value
	// inherited from a parent type shows in Required.Source instead.
	Override *int `json:"override"`
}

// HeadcountDefaults lists every event type's requirement per department,
// parents before their subtypes.
func HeadcountDefaults(ctx context.Context, q store.DBTX) ([]TypeHeadcount, error) {
	types, err := shifts.ListEventTypes(ctx, q)
	if err != nil {
		return nil, err
	}
	depts, err := ListDepartments(ctx, q)
	if err != nil {
		return nil, err
	}
	children := map[entity.ID][]shifts.EventType{}
	var roots []shifts.EventType
	for _, t := range types {
		if t.ParentID.Valid {
			children[t.ParentID.ID] = append(children[t.ParentID.ID], t)
		} else {
			roots = append(roots, t)
		}
	}
	var out []TypeHeadcount
	var walk func(ts []shifts.EventType, depth int) error
	walk = func(ts []shifts.EventType, depth int) error {
		for _, t := range ts {
			chain, err := eventTypeChain(ctx, q, t.ID)
			if err != nil {
				return err
			}
			th := TypeHeadcount{EventTypeID: t.ID, Name: t.Name, Depth: depth}
			for _, d := range depts {
				req, _, _, err := requirement(ctx, q, d.ID, entity.NullID{}, chain)
				if err != nil {
					return err
				}
				tr := TypeRequirement{DepartmentID: d.ID, Department: d.Name, Required: req}
				var n int
				err = q.QueryRowContext(ctx, `SELECT headcount FROM headcount_overrides
					WHERE role_id = ? AND event_type_id = ?`, d.ID, t.ID).Scan(&n)
				switch {
				case err == nil:
					tr.Override = &n
				case !errors.Is(err, sql.ErrNoRows):
					return err
				}
				th.Departments = append(th.Departments, tr)
			}
			out = append(out, th)
			if err := walk(children[t.ID], depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(roots, 0)
}
