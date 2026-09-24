package http

import (
	"errors"
	"fmt"
	"net/http"

	"lsm/internal/auth"
	"lsm/internal/staff"
)

func init() {
	errorMappers = append(errorMappers, func(err error) *apiError {
		switch {
		case errors.Is(err, auth.ErrWeakPassword):
			return &apiError{http.StatusBadRequest, "weak_password",
				fmt.Sprintf("password must be at least %d characters", auth.MinPasswordLength)}
		case errors.Is(err, auth.ErrSamePassword):
			return &apiError{http.StatusBadRequest, "same_password", "new password must differ from the current one"}
		}
		return nil
	})
}

func (a *api) authRoutes(mux *http.ServeMux) {
	// Sign-in is the one unauthenticated endpoint.
	mux.HandleFunc("POST /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decode(r, &in); err != nil {
			writeError(w, a.log, err)
			return
		}
		token, p, err := auth.SignIn(r.Context(), a.db, in.Username, in.Password, a.now())
		if err != nil {
			writeError(w, a.log, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token":                token,
			"must_change_password": p.MustChangePassword,
			"person":               map[string]any{"id": p.PersonID, "name": p.Name, "tier": p.Tier},
		})
	})

	a.route(mux, "DELETE /api/v1/session", passwordChange, func(r *http.Request, p auth.Principal) (any, error) {
		return map[string]bool{"signed_out": true}, auth.SignOut(r.Context(), a.db, p.SessionID, a.now())
	})

	a.route(mux, "GET /api/v1/me", passwordChange, func(r *http.Request, p auth.Principal) (any, error) {
		return a.me(r, p)
	})

	a.route(mux, "POST /api/v1/me/password", passwordChange, func(r *http.Request, p auth.Principal) (any, error) {
		var in struct {
			Current string `json:"current_password"`
			New     string `json:"new_password"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if err := auth.ChangePassword(r.Context(), a.db, p, in.Current, in.New, a.now()); err != nil {
			if errors.Is(err, auth.ErrBadCredentials) {
				return nil, &apiError{http.StatusBadRequest, "wrong_password", "current password is incorrect"}
			}
			return nil, err
		}
		p.MustChangePassword = false
		return a.me(r, p)
	})
}

// meView is a person's own profile. It carries no gender and no hidden
// preferences: staff never see either, even about themselves.
type meView struct {
	ID                 any    `json:"id"`
	Name               string `json:"name"`
	Badge              string `json:"badge"`
	Email              string `json:"email"`
	Tier               string `json:"tier"`
	Role               string `json:"role"`
	Department         string `json:"department"`
	Tenure             string `json:"tenure_years"`
	MustChangePassword bool   `json:"must_change_password"`
}

func (a *api) me(r *http.Request, p auth.Principal) (meView, error) {
	person, err := staff.PersonByID(r.Context(), a.db, p.PersonID)
	if err != nil {
		return meView{}, err
	}
	roles, err := staff.ListRoles(r.Context(), a.db)
	if err != nil {
		return meView{}, err
	}
	var role string
	for _, ro := range roles {
		if ro.ID == person.RoleID {
			role = ro.Name
		}
	}
	return meView{
		ID: person.ID, Name: person.Name, Badge: person.Badge, Email: person.Email,
		Tier: string(person.Tier), Role: role, Department: departmentOf(roles, person.RoleID).Name,
		Tenure: person.Tenure(a.now()).String(), MustChangePassword: p.MustChangePassword,
	}, nil
}
