package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"lsm/internal/entity"
	"lsm/internal/staff"
	"lsm/internal/store"
)

// SessionLifetime is how long a token lives without use. Each use slides
// the expiry, so a device in daily use stays signed in.
const SessionLifetime = 180 * 24 * time.Hour

// ErrBadCredentials is returned for an unknown user or wrong password,
// deliberately without saying which.
var ErrBadCredentials = errors.New("auth: invalid username or password")

// ErrSamePassword rejects a password change to the current password.
var ErrSamePassword = errors.New("auth: new password must differ from the current one")

// ErrNoSession means the token is unknown, revoked or expired.
var ErrNoSession = errors.New("auth: not signed in")

// Principal is the signed-in person behind a request.
type Principal struct {
	SessionID          entity.ID
	PersonID           entity.ID
	Name               string
	Tier               staff.Tier
	MustChangePassword bool
	// Station is set, and the person fields are empty, when the caller
	// is an unattended kiosk rather than a signed-in person.
	Station *Station
}

// IsStation reports a kiosk rather than a person.
func (p Principal) IsStation() bool { return p.Station != nil }

// IsAdmin reports admin tier.
func (p Principal) IsAdmin() bool { return !p.IsStation() && p.Tier == staff.TierAdmin }

// IsSupervisor reports supervisor tier or above.
func (p Principal) IsSupervisor() bool {
	return !p.IsStation() && (p.Tier == staff.TierSupervisor || p.Tier == staff.TierAdmin)
}

// SignIn checks a username (email) and password and opens a session,
// returning the bearer token. The token is shown once and never stored.
func SignIn(ctx context.Context, db *sql.DB, username, password string, now time.Time) (string, Principal, error) {
	var (
		personID entity.ID
		hash     string
		active   bool
	)
	err := db.QueryRowContext(ctx, `SELECT c.person_id, c.password_hash, p.active
		FROM credentials c JOIN persons p ON p.id = c.person_id WHERE c.username = ?`,
		strings.ToLower(strings.TrimSpace(username))).Scan(&personID, &hash, &active)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend comparable time so response timing does not reveal
		// which usernames exist.
		_, _ = VerifyPassword(dummyHash(), password)
		return "", Principal{}, ErrBadCredentials
	}
	if err != nil {
		return "", Principal{}, err
	}
	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return "", Principal{}, err
	}
	if !ok || !active {
		return "", Principal{}, ErrBadCredentials
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Principal{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	id := entity.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions
		(id, person_id, token_hash, created_at, last_used_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, personID, tokenHash(token), entity.FormatTime(now), entity.FormatTime(now),
		entity.FormatTime(now.Add(SessionLifetime))); err != nil {
		return "", Principal{}, err
	}
	p, err := lookup(ctx, db, `s.id = ?`, id)
	return token, p, err
}

// dummyHash is verified against when the username is unknown.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("not-a-real-password")
	return h
})

// Authenticate resolves a bearer token to its principal and slides the
// session's expiry.
func Authenticate(ctx context.Context, db *sql.DB, token string, now time.Time) (Principal, error) {
	if token == "" {
		return Principal{}, ErrNoSession
	}
	if strings.HasPrefix(token, "st_") {
		return authenticateStation(ctx, db, token, now)
	}
	p, err := lookup(ctx, db, `s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ? AND per.active = 1`,
		tokenHash(token), entity.FormatTime(now))
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrNoSession
	}
	if err != nil {
		return Principal{}, err
	}
	_, err = db.ExecContext(ctx, `UPDATE sessions SET last_used_at = ?, expires_at = ? WHERE id = ?`,
		entity.FormatTime(now), entity.FormatTime(now.Add(SessionLifetime)), p.SessionID)
	return p, err
}

func lookup(ctx context.Context, db *sql.DB, where string, args ...any) (Principal, error) {
	var p Principal
	err := db.QueryRowContext(ctx, `SELECT s.id, per.id, per.name, per.tier, c.must_change_password
		FROM sessions s
		JOIN persons per ON per.id = s.person_id
		JOIN credentials c ON c.person_id = per.id
		WHERE `+where, args...).Scan(&p.SessionID, &p.PersonID, &p.Name, &p.Tier, &p.MustChangePassword)
	return p, err
}

// SignOut revokes one session.
func SignOut(ctx context.Context, db *sql.DB, sessionID entity.ID, now time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		entity.FormatTime(now), sessionID)
	return err
}

// ChangePassword replaces a person's password after checking the current
// one, clears the forced-change flag, and revokes their other sessions.
func ChangePassword(ctx context.Context, db *sql.DB, p Principal, current, next string, now time.Time) error {
	if next == current {
		return ErrSamePassword
	}
	if len([]rune(next)) < MinPasswordLength {
		return ErrWeakPassword
	}
	var hash string
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM credentials WHERE person_id = ?`,
		p.PersonID).Scan(&hash); err != nil {
		return err
	}
	ok, err := VerifyPassword(hash, current)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadCredentials
	}
	newHash, err := HashPassword(next)
	if err != nil {
		return err
	}
	return store.InTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE credentials
			SET password_hash = ?, must_change_password = 0, updated_at = ? WHERE person_id = ?`,
			newHash, entity.FormatTime(now), p.PersonID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ?
			WHERE person_id = ? AND id <> ? AND revoked_at IS NULL`,
			entity.FormatTime(now), p.PersonID, p.SessionID)
		return err
	})
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
