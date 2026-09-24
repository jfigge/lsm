package http

import (
	"errors"
	"net/http"

	"lsm/internal/auth"
	"lsm/internal/entity"
	"lsm/internal/shifts"
)

func init() {
	errorMappers = append(errorMappers, func(err error) *apiError {
		switch {
		case errors.Is(err, shifts.ErrNoRoster):
			return &apiError{http.StatusConflict, "no_roster", "Commit the event's roster before running the matcher."}
		case errors.Is(err, shifts.ErrWrongShift):
			return &apiError{http.StatusBadRequest, "wrong_shift", "That post is not staffed in that shift."}
		case errors.Is(err, shifts.ErrNotFound):
			return &apiError{http.StatusNotFound, "not_found", "not found"}
		}
		return nil
	})
}

func (a *api) assignRoutes(mux *http.ServeMux) {
	board := func(r *http.Request, eventID entity.ID) (any, error) {
		return shifts.AssignmentBoard(r.Context(), a.db, eventID, a.opt.Location)
	}
	withEvent := func(do func(r *http.Request, p auth.Principal, eventID entity.ID) error) handler {
		return func(r *http.Request, p auth.Principal) (any, error) {
			eventID, err := pathID(r, "event")
			if err != nil {
				return nil, err
			}
			if do != nil {
				if err := do(r, p, eventID); err != nil {
					return nil, err
				}
			}
			return board(r, eventID)
		}
	}

	// The board is also the printed sheet's data.
	a.route(mux, "GET /api/v1/events/{event}/assignments", supervisorOnly, withEvent(nil))

	a.route(mux, "POST /api/v1/events/{event}/match", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		var in struct {
			PresentOnly bool `json:"present_only"`
		}
		if r.ContentLength > 0 {
			if err := decode(r, &in); err != nil {
				return nil, err
			}
		}
		sum, err := shifts.RunMatcher(r.Context(), a.db, eventID, shifts.MatchOptions{PresentOnly: in.PresentOnly}, p.PersonID, a.now())
		if err != nil {
			return nil, err
		}
		b, err := board(r, eventID)
		return map[string]any{"summary": sum, "board": b}, err
	})

	a.route(mux, "POST /api/v1/events/{event}/assignments", adminOnly, withEvent(func(r *http.Request, p auth.Principal, eventID entity.ID) error {
		var in struct {
			PositionID entity.ID `json:"position_id"`
			PersonID   entity.ID `json:"person_id"`
			Shift      string    `json:"shift"`
			Note       string    `json:"note"`
		}
		if err := decode(r, &in); err != nil {
			return err
		}
		return shifts.Place(r.Context(), a.db, eventID, in.PositionID, in.PersonID, in.Shift, p.PersonID, in.Note, a.now())
	}))

	a.route(mux, "DELETE /api/v1/events/{event}/assignments/{assignment}", adminOnly, withEvent(func(r *http.Request, p auth.Principal, eventID entity.ID) error {
		id, err := pathID(r, "assignment")
		if err != nil {
			return err
		}
		return shifts.Unassign(r.Context(), a.db, eventID, id, p.PersonID, a.now())
	}))

	a.route(mux, "PUT /api/v1/events/{event}/assignments/{assignment}/pin", adminOnly, withEvent(func(r *http.Request, p auth.Principal, eventID entity.ID) error {
		id, err := pathID(r, "assignment")
		if err != nil {
			return err
		}
		var in struct {
			Pinned bool `json:"pinned"`
		}
		if err := decode(r, &in); err != nil {
			return err
		}
		return shifts.Pin(r.Context(), a.db, eventID, id, in.Pinned)
	}))

	a.route(mux, "PUT /api/v1/events/{event}/published", adminOnly, withEvent(func(r *http.Request, p auth.Principal, eventID entity.ID) error {
		var in struct {
			Published bool `json:"published"`
		}
		if err := decode(r, &in); err != nil {
			return err
		}
		return shifts.Publish(r.Context(), a.db, eventID, in.Published, p.PersonID, a.now())
	}))
}
