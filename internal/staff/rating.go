package staff

import (
	"context"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// Rating is a supervisor's end-of-night band for one person (SPEC §10.4).
type Rating string

const (
	RatingExceptional     Rating = "exceptional"
	RatingAboveAverage    Rating = "above_average"
	RatingAcceptable      Rating = "acceptable" // the slider default
	RatingBelowPar        Rating = "below_par"
	RatingNeedsDiscipline Rating = "needs_discipline"
)

// Valid reports whether r is one of the five bands.
func (r Rating) Valid() bool {
	switch r {
	case RatingExceptional, RatingAboveAverage, RatingAcceptable, RatingBelowPar, RatingNeedsDiscipline:
		return true
	}
	return false
}

// RatingSource records how an entry was made.
type RatingSource string

const (
	SourceSeed  RatingSource = "seed"
	SourceApp   RatingSource = "app"
	SourceAdmin RatingSource = "admin"
)

// RatingEntry is one rating as given. Entries are employment records and
// are never updated: a change is a new entry, and the latest per person
// and event is the one in force (the ratings_current view).
type RatingEntry struct {
	ID       entity.ID
	PersonID entity.ID
	EventID  entity.ID
	Rating   Rating
	RatedBy  entity.NullID
	RatedAt  time.Time
	Source   RatingSource
}

// AppendRating records a rating entry.
func AppendRating(ctx context.Context, q store.DBTX, r RatingEntry) error {
	if !r.Rating.Valid() {
		return fmt.Errorf("staff: invalid rating %q", r.Rating)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO rating_entries
		(id, person_id, event_id, rating, rated_by, rated_at, source) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.PersonID, r.EventID, r.Rating, r.RatedBy, entity.FormatTime(r.RatedAt), r.Source)
	if err != nil {
		return fmt.Errorf("staff: append rating: %w", err)
	}
	return nil
}

// RatingHistory returns every entry for a person, newest first: the audit
// trail a person can be shown for their own record.
func RatingHistory(ctx context.Context, q store.DBTX, personID entity.ID) ([]RatingEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, person_id, event_id, rating, rated_by, rated_at, source
		FROM rating_entries WHERE person_id = ? ORDER BY rated_at DESC, id DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatingEntry
	for rows.Next() {
		var (
			r  RatingEntry
			at string
		)
		if err := rows.Scan(&r.ID, &r.PersonID, &r.EventID, &r.Rating, &r.RatedBy, &at, &r.Source); err != nil {
			return nil, err
		}
		if r.RatedAt, err = entity.ParseTime(at); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
