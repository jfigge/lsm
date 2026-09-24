package http

import (
	"errors"
	"math"
	"net/http"

	"lsm/internal/auth"
	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/signup"
	"lsm/internal/staff"
)

func init() {
	errorMappers = append(errorMappers, func(err error) *apiError {
		var in *signup.InputError
		switch {
		case errors.As(err, &in):
			return &apiError{http.StatusBadRequest, "invalid_request", in.Error()}
		case errors.Is(err, signup.ErrNotFound):
			return &apiError{http.StatusNotFound, "not_found", "not found"}
		case errors.Is(err, signup.ErrMonthNotOpen):
			return &apiError{http.StatusConflict, "month_not_open", "this month is not open for signups"}
		}
		return nil
	})
}

func departmentOf(roles []staff.Role, roleID entity.ID) staff.Role {
	return signup.Departments(roles)[roleID]
}

func (a *api) signupRoutes(mux *http.ServeMux) {
	// ------------------------------------------------------------ months --

	a.route(mux, "GET /api/v1/months", signedIn, func(r *http.Request, p auth.Principal) (any, error) {
		return signup.ListMonths(r.Context(), a.db, p.IsAdmin())
	})

	a.route(mux, "PUT /api/v1/months/{month}", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		var in struct {
			Status shifts.MonthStatus `json:"status"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		month := r.PathValue("month")
		if err := signup.SetMonthStatus(r.Context(), a.db, month, in.Status, p.PersonID, a.now(), a.opt.Notifier); err != nil {
			return nil, err
		}
		status, err := signup.MonthStatus(r.Context(), a.db, month)
		return map[string]any{"month": month, "status": status}, err
	})

	a.route(mux, "GET /api/v1/months/{month}/events", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		return signup.MonthOverview(r.Context(), a.db, r.PathValue("month"))
	})

	a.route(mux, "GET /api/v1/departments", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		return signup.ListDepartments(r.Context(), a.db)
	})

	a.route(mux, "GET /api/v1/headcount", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		return signup.HeadcountDefaults(r.Context(), a.db)
	})

	// ------------------------------------------------- own availability --

	a.route(mux, "GET /api/v1/availability/{month}", signedIn, func(r *http.Request, p auth.Principal) (any, error) {
		return signup.PersonMonth(r.Context(), a.db, p.PersonID, r.PathValue("month"), a.opt.Location)
	})

	a.route(mux, "PUT /api/v1/events/{event}/availability", signedIn, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		var in signup.Selection
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		card, rate, err := signup.SetAvailability(r.Context(), a.db, p.PersonID, eventID, in, a.now(), a.opt.Location)
		if err != nil {
			return nil, err
		}
		return map[string]any{"event": card, "rate": rate}, nil
	})

	// ------------------------------------------------------------ ranking --

	// The full ranked list carries every person's performance score, so
	// it is for supervisors and admins; staff get their own row only.
	a.route(mux, "GET /api/v1/events/{event}/ranking", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		w, err := signup.LoadWeights(r.Context(), a.db)
		if err != nil {
			return nil, err
		}
		ranking, err := signup.RankEvent(r.Context(), a.db, eventID, w)
		if err != nil {
			return nil, err
		}
		windows, err := signup.EventWindows(r.Context(), a.db, eventID, a.opt.Location)
		return map[string]any{"ranking": ranking, "windows": windows}, err
	})

	a.route(mux, "GET /api/v1/events/{event}/ranking/me", signedIn, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		return signup.RankFor(r.Context(), a.db, eventID, p.PersonID)
	})

	a.route(mux, "GET /api/v1/ranking/rules", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		return a.rules(r)
	})

	a.route(mux, "PUT /api/v1/ranking/rules", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		var in struct {
			Weights     signup.Weights `json:"weights"`
			Expectation *float64       `json:"availability_expectation_percent"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if len(in.Weights) > 0 {
			if _, err := signup.SaveWeights(r.Context(), a.db, in.Weights, p.PersonID, a.now()); err != nil {
				return nil, err
			}
		}
		if in.Expectation != nil {
			if err := signup.SetExpectation(r.Context(), a.db, *in.Expectation/100, a.now()); err != nil {
				return nil, err
			}
		}
		return a.rules(r)
	})

	// Preview: rank under current and proposed weights and show who
	// crosses the line in each direction. Nothing is saved.
	a.route(mux, "POST /api/v1/ranking/preview", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		var in struct {
			Weights     signup.Weights `json:"weights"`
			Expectation *float64       `json:"availability_expectation_percent"`
			Month       string         `json:"month"`
			EventID     *entity.ID     `json:"event_id"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		current, err := signup.LoadWeights(r.Context(), a.db)
		if err != nil {
			return nil, err
		}
		proposed := current.Clone()
		for k, v := range in.Weights {
			proposed[k] = v
		}
		var ids []entity.ID
		switch {
		case in.EventID != nil && in.Month == "":
			ids = []entity.ID{*in.EventID}
		case in.EventID == nil && in.Month != "":
			if ids, err = signup.EventIDsInMonth(r.Context(), a.db, in.Month); err != nil {
				return nil, err
			}
		default:
			return nil, badRequest("give either month or event_id")
		}
		prop := signup.Proposal{Weights: proposed}
		if in.Expectation != nil {
			e := *in.Expectation / 100
			prop.Expectation = &e
		}
		events, err := signup.Preview(r.Context(), a.db, ids, current, prop)
		if err != nil {
			return nil, err
		}
		return map[string]any{"current": current, "proposed": proposed, "events": events}, nil
	})

	// ---------------------------------------------------- admin controls --

	a.route(mux, "POST /api/v1/events/{event}/overrides", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		var in struct {
			PersonID entity.ID `json:"person_id"`
			Note     string    `json:"note"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if err := signup.PullAboveLine(r.Context(), a.db, eventID, in.PersonID, p.PersonID, in.Note, a.now()); err != nil {
			return nil, err
		}
		return signup.RankFor(r.Context(), a.db, eventID, in.PersonID)
	})

	a.route(mux, "DELETE /api/v1/events/{event}/overrides/{person}", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		personID, err := pathID(r, "person")
		if err != nil {
			return nil, err
		}
		if err := signup.WithdrawOverride(r.Context(), a.db, eventID, personID, p.PersonID, a.now()); err != nil {
			return nil, err
		}
		return signup.RankFor(r.Context(), a.db, eventID, personID)
	})

	headcount := func(scope string) handler {
		return func(r *http.Request, p auth.Principal) (any, error) {
			id, err := pathID(r, scope)
			if err != nil {
				return nil, err
			}
			var in struct {
				DepartmentID entity.ID `json:"department_id"`
				Headcount    *int      `json:"headcount"` // null clears the override
			}
			if err := decode(r, &in); err != nil {
				return nil, err
			}
			var typeID, eventID entity.NullID
			if scope == "event" {
				eventID = entity.Some(id)
			} else {
				typeID = entity.Some(id)
			}
			if err := signup.SetHeadcount(r.Context(), a.db, in.DepartmentID, typeID, eventID,
				in.Headcount, p.PersonID, a.now()); err != nil {
				return nil, err
			}
			return map[string]any{"department_id": in.DepartmentID, "headcount": in.Headcount}, nil
		}
	}
	a.route(mux, "PUT /api/v1/events/{event}/headcount", adminOnly, headcount("event"))
	a.route(mux, "PUT /api/v1/event-types/{type}/headcount", adminOnly, headcount("type"))

	// Commit the recommendation as the event's roster, which the matcher
	// consumes. Re-committing after changes is safe.
	a.route(mux, "POST /api/v1/events/{event}/roster", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		return signup.CommitRoster(r.Context(), a.db, eventID)
	})
}

type ruleView struct {
	Rule        string `json:"rule"`
	Weight      int    `json:"weight"`
	Description string `json:"description"`
}

func (a *api) rules(r *http.Request) (any, error) {
	w, err := signup.LoadWeights(r.Context(), a.db)
	if err != nil {
		return nil, err
	}
	exp, err := signup.Expectation(r.Context(), a.db)
	if err != nil {
		return nil, err
	}
	out := make([]ruleView, 0, len(signup.Rules))
	for _, name := range signup.Rules {
		out = append(out, ruleView{Rule: name, Weight: w[name], Description: signup.RuleDescriptions[name]})
	}
	return map[string]any{
		"rules":                            out,
		"tiebreak":                         "earliest signup",
		"availability_expectation_percent": math.Round(exp * 100),
	}, nil
}
