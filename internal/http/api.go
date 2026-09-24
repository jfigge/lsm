package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"lsm/internal/auth"
	"lsm/internal/entity"
)

// api is the JSON API under /api/v1. The same endpoints serve the iOS app
// and the web pages, so the /supervisor web page stays possible as a
// fallback (SPEC §10.1).
type api struct {
	db  *sql.DB
	log *slog.Logger
	opt Options
	now func() time.Time
}

// handler is an API endpoint: it returns a value to encode as JSON, or an
// error mapped to a status by writeError.
type handler func(r *http.Request, p auth.Principal) (any, error)

// access says who may call an endpoint.
type access int

const (
	signedIn access = iota
	supervisorOnly
	adminOnly
	// passwordChange is reachable while a forced password change is
	// pending; everything else is not.
	passwordChange
	// kioskStation is for paired kiosks only. Every other level refuses
	// a station, so a kiosk token can scan badges and nothing more.
	kioskStation
)

func (a *api) route(mux *http.ServeMux, pattern string, level access, h handler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticate(r)
		if err != nil {
			writeError(w, a.log, err)
			return
		}
		switch {
		case p.IsStation() != (level == kioskStation):
			writeError(w, a.log, errForbidden)
			return
		case p.MustChangePassword && level != passwordChange:
			writeError(w, a.log, errPasswordChange)
			return
		case level == supervisorOnly && !p.IsSupervisor(), level == adminOnly && !p.IsAdmin():
			writeError(w, a.log, errForbidden)
			return
		}
		v, err := h(r, p)
		if err != nil {
			writeError(w, a.log, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}

func (a *api) authenticate(r *http.Request) (auth.Principal, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return auth.Principal{}, auth.ErrNoSession
	}
	return auth.Authenticate(r.Context(), a.db, strings.TrimSpace(token), a.now())
}

// ---------------------------------------------------------------- errors --

// apiError is an error with a status and a stable machine-readable code.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

var (
	errForbidden      = &apiError{http.StatusForbidden, "forbidden", "not permitted for your account"}
	errPasswordChange = &apiError{http.StatusForbidden, "password_change_required",
		"change your password before continuing"}
)

func badRequest(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, "invalid_request", fmt.Sprintf(format, args...)}
}

// errorMappers translate domain errors to API errors; registered by the
// files that own each domain.
var errorMappers []func(error) *apiError

func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		for _, m := range errorMappers {
			if ae = m(err); ae != nil {
				break
			}
		}
	}
	switch {
	case ae != nil:
	case errors.Is(err, auth.ErrNoSession):
		ae = &apiError{http.StatusUnauthorized, "unauthenticated", "sign in to continue"}
	case errors.Is(err, auth.ErrBadCredentials):
		ae = &apiError{http.StatusUnauthorized, "bad_credentials", "invalid username or password"}
	default:
		log.Error("api", "err", err)
		ae = &apiError{http.StatusInternalServerError, "internal", "something went wrong"}
	}
	writeJSON(w, ae.status, map[string]any{"error": map[string]string{"code": ae.code, "message": ae.msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(v)
}

// decode reads a JSON request body strictly.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("request body: %v", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (entity.ID, error) {
	id, err := entity.ParseID(r.PathValue(name))
	if err != nil {
		return entity.Nil, &apiError{http.StatusNotFound, "not_found", "no such " + name}
	}
	return id, nil
}
