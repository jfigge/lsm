package http

import (
	"errors"
	"net/http"
	"strings"

	"lsm/internal/auth"
	"lsm/internal/checkin"
	"lsm/internal/entity"
	"lsm/internal/shifts"
)

func init() {
	errorMappers = append(errorMappers, func(err error) *apiError {
		msg := err.Error()
		if i := strings.Index(msg, ": "); i >= 0 {
			msg = msg[i+2:]
		}
		switch {
		case errors.Is(err, checkin.ErrBadgeUnknown):
			return &apiError{http.StatusNotFound, "badge_unknown", "Badge not recognised"}
		case errors.Is(err, checkin.ErrNotFound):
			return &apiError{http.StatusNotFound, "not_found", "not found"}
		case errors.Is(err, checkin.ErrNotCheckedIn):
			return &apiError{http.StatusConflict, "not_checked_in", "not checked in"}
		case errors.Is(err, checkin.ErrNoneAvailable):
			return &apiError{http.StatusConflict, "none_available", msg}
		case errors.Is(err, checkin.ErrInput):
			return &apiError{http.StatusBadRequest, "invalid_request", msg}
		case errors.Is(err, auth.ErrStationName):
			return &apiError{http.StatusBadRequest, "invalid_request", "a station needs a name"}
		}
		return nil
	})
}

type eventView struct {
	ID   entity.ID `json:"id"`
	Name string    `json:"name"`
	Date string    `json:"date"`
}

func eventViews(events []shifts.Event) []eventView {
	out := make([]eventView, 0, len(events))
	for _, e := range events {
		out = append(out, eventView{ID: e.ID, Name: e.Name, Date: entity.FormatDate(e.Date)})
	}
	return out
}

func (a *api) checkinRoutes(mux *http.ServeMux) {
	// -------------------------------------------------------- stations --

	a.route(mux, "POST /api/v1/stations", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		token, s, err := auth.CreateStation(r.Context(), a.db, in.Name, auth.StationKiosk, p.PersonID, a.now())
		return map[string]any{"token": token, "station": s}, err
	})
	a.route(mux, "GET /api/v1/stations", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		return auth.ListStations(r.Context(), a.db)
	})
	a.route(mux, "DELETE /api/v1/stations/{station}", adminOnly, func(r *http.Request, p auth.Principal) (any, error) {
		id, err := pathID(r, "station")
		if err != nil {
			return nil, err
		}
		return map[string]bool{"revoked": true}, auth.RevokeStation(r.Context(), a.db, id, a.now())
	})

	// ----------------------------------------------------------- kiosk --

	a.route(mux, "GET /api/v1/kiosk", kioskStation, func(r *http.Request, p auth.Principal) (any, error) {
		events, err := checkin.TodaysEvents(r.Context(), a.db, a.now(), a.opt.Location)
		return map[string]any{"station": p.Station.Name, "today": eventViews(events)}, err
	})
	a.route(mux, "GET /api/v1/kiosk/event/{event}", kioskStation, func(r *http.Request, p auth.Principal) (any, error) {
		id, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		e, err := shifts.EventByID(r.Context(), a.db, id)
		if err != nil {
			return nil, checkin.ErrNotFound
		}
		return eventViews([]shifts.Event{e})[0], nil
	})
	a.route(mux, "POST /api/v1/kiosk/scan", kioskStation, func(r *http.Request, p auth.Principal) (any, error) {
		var in struct {
			EventID entity.ID `json:"event_id"`
			Badge   string    `json:"badge"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return checkin.KioskScan(r.Context(), a.db, in.EventID, in.Badge, p.Station.ID, a.now(), a.opt.Location)
	})

	// ------------------------------------------------------- reception --

	// Events to run reception for: today's, plus a window around today so
	// a demo or a late reconciliation can pick another night.
	a.route(mux, "GET /api/v1/reception/events", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		today, err := checkin.TodaysEvents(r.Context(), a.db, a.now(), a.opt.Location)
		if err != nil {
			return nil, err
		}
		all, err := shifts.ListEvents(r.Context(), a.db)
		if err != nil {
			return nil, err
		}
		from, to := a.now().AddDate(0, 0, -14), a.now().AddDate(0, 0, 60)
		var near []shifts.Event
		for _, e := range all {
			if e.Date.After(from) && e.Date.Before(to) {
				near = append(near, e)
			}
		}
		return map[string]any{"today": eventViews(today), "nearby": eventViews(near)}, nil
	})

	a.route(mux, "GET /api/v1/reception/search", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		return checkin.SearchPeople(r.Context(), a.db, r.URL.Query().Get("q"), 12)
	})

	a.route(mux, "GET /api/v1/events/{event}/scan/{badge}", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		personID, err := checkin.ResolveBadge(r.Context(), a.db, r.PathValue("badge"))
		if err != nil {
			return nil, err
		}
		return checkin.DeskFor(r.Context(), a.db, eventID, personID, a.opt.Location)
	})

	desk := func(do func(r *http.Request, p auth.Principal, eventID, personID entity.ID) (string, error)) handler {
		return func(r *http.Request, p auth.Principal) (any, error) {
			eventID, err := pathID(r, "event")
			if err != nil {
				return nil, err
			}
			personID, err := pathID(r, "person")
			if err != nil {
				return nil, err
			}
			warning := ""
			if do != nil {
				if warning, err = do(r, p, eventID, personID); err != nil {
					return nil, err
				}
			}
			d, err := checkin.DeskFor(r.Context(), a.db, eventID, personID, a.opt.Location)
			return map[string]any{"desk": d, "warning": warning}, err
		}
	}
	reception := func(p auth.Principal) checkin.Where {
		return checkin.Where{Location: "reception", By: entity.Some(p.PersonID)}
	}

	a.route(mux, "GET /api/v1/events/{event}/desk/{person}", supervisorOnly, desk(nil))

	// End-of-night reconciliation: what went out and has not come back.
	a.route(mux, "GET /api/v1/events/{event}/unreturned", supervisorOnly, func(r *http.Request, p auth.Principal) (any, error) {
		eventID, err := pathID(r, "event")
		if err != nil {
			return nil, err
		}
		return checkin.Unreturned(r.Context(), a.db, eventID, a.now(), a.opt.Location)
	})

	// Idempotent: someone already in keeps their original time and place.
	a.route(mux, "POST /api/v1/events/{event}/desk/{person}/checkin", supervisorOnly,
		desk(func(r *http.Request, p auth.Principal, e, person entity.ID) (string, error) {
			_, err := checkin.CheckIn(r.Context(), a.db, e, person, reception(p), a.now())
			return "", err
		}))

	a.route(mux, "POST /api/v1/events/{event}/desk/{person}/checkout", supervisorOnly,
		desk(func(r *http.Request, p auth.Principal, e, person entity.ID) (string, error) {
			return "", checkin.CheckOut(r.Context(), a.db, e, person, reception(p), a.now())
		}))

	a.route(mux, "POST /api/v1/events/{event}/desk/{person}/issues", supervisorOnly,
		desk(func(r *http.Request, p auth.Principal, e, person entity.ID) (string, error) {
			var in checkin.IssueRequest
			if err := decode(r, &in); err != nil {
				return "", err
			}
			return checkin.Issue(r.Context(), a.db, e, person, in, p.PersonID, a.now())
		}))

	a.route(mux, "POST /api/v1/events/{event}/desk/{person}/issues/{issue}/return", supervisorOnly,
		desk(func(r *http.Request, p auth.Principal, e, person entity.ID) (string, error) {
			id, err := pathID(r, "issue")
			if err != nil {
				return "", err
			}
			return "", checkin.Return(r.Context(), a.db, e, person, id, p.PersonID, a.now())
		}))
}
