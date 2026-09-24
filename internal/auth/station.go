package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"lsm/internal/entity"
)

// Station is an unattended screen (a kiosk) holding its own token. A
// station can do exactly what its kind allows and nothing else.
type Station struct {
	ID        entity.ID  `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen_at"`
	Revoked   bool       `json:"revoked"`
}

// StationKiosk is the only kind so far.
const StationKiosk = "kiosk"

// ErrStationName rejects an empty station name.
var ErrStationName = errors.New("auth: a station needs a name")

// CreateStation registers a station and returns its token, shown once.
func CreateStation(ctx context.Context, db *sql.DB, name, kind string, by entity.ID, now time.Time) (string, Station, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Station{}, ErrStationName
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Station{}, err
	}
	token := "st_" + base64.RawURLEncoding.EncodeToString(raw)
	s := Station{ID: entity.NewID(), Name: name, Kind: kind, CreatedAt: now}
	_, err := db.ExecContext(ctx, `INSERT INTO stations (id, name, kind, token_hash, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?)`, s.ID, name, kind, tokenHash(token), entity.FormatTime(now), by)
	return token, s, err
}

// ListStations returns every station, newest first.
func ListStations(ctx context.Context, db *sql.DB) ([]Station, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, kind, created_at, last_seen_at, revoked_at IS NOT NULL
		FROM stations ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Station{}
	for rows.Next() {
		var (
			s        Station
			created  string
			lastSeen sql.NullString
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.Kind, &created, &lastSeen, &s.Revoked); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = entity.ParseTime(created)
		if lastSeen.Valid {
			t, _ := entity.ParseTime(lastSeen.String)
			s.LastSeen = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeStation stops a station's token working.
func RevokeStation(ctx context.Context, db *sql.DB, id entity.ID, now time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE stations SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		entity.FormatTime(now), id)
	return err
}

// authenticateStation resolves a station token.
func authenticateStation(ctx context.Context, db *sql.DB, token string, now time.Time) (Principal, error) {
	var s Station
	err := db.QueryRowContext(ctx, `SELECT id, name, kind FROM stations WHERE token_hash = ? AND revoked_at IS NULL`,
		tokenHash(token)).Scan(&s.ID, &s.Name, &s.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrNoSession
	}
	if err != nil {
		return Principal{}, err
	}
	_, err = db.ExecContext(ctx, `UPDATE stations SET last_seen_at = ? WHERE id = ?`, entity.FormatTime(now), s.ID)
	return Principal{Name: s.Name, Station: &s}, err
}
