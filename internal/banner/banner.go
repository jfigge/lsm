// Package banner resolves the top-of-screen message a person sees for an
// event (SPEC §2 "Banners").
//
// Banners have no validity windows. Two independent axes decide whether
// one applies: who (everyone, or roles at any level of the hierarchy —
// a banner on a role reaches everything beneath it) and when (an event
// type, inherited by its subtypes, or a single event). The most specific
// "when" wins: a banner on the event itself, else the nearest event type
// walking up the hierarchy, else none.
package banner

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// Banner is a resolved message.
type Banner struct {
	ID   entity.ID `json:"id"`
	Text string    `json:"text"`
	// Scope is "event" or "event_type".
	Scope string `json:"scope"`
}

// For returns the banner a person in roleID sees for an event, or nil.
func For(ctx context.Context, q store.DBTX, roleID, eventID entity.ID) (*Banner, error) {
	roles, err := chain(ctx, q, `SELECT parent_id FROM roles WHERE id = ?`, roleID)
	if err != nil {
		return nil, err
	}
	var typeID entity.ID
	if err := q.QueryRowContext(ctx, `SELECT event_type_id FROM events WHERE id = ?`, eventID).Scan(&typeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if b, err := pick(ctx, q, `JOIN banner_events be ON be.banner_id = b.id AND be.event_id = ?`, eventID, roles); b != nil || err != nil {
		if b != nil {
			b.Scope = "event"
		}
		return b, err
	}
	types, err := chain(ctx, q, `SELECT parent_id FROM event_types WHERE id = ?`, typeID)
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		b, err := pick(ctx, q, `JOIN banner_event_types bt ON bt.banner_id = b.id AND bt.event_type_id = ?`, t, roles)
		if b != nil || err != nil {
			if b != nil {
				b.Scope = "event_type"
			}
			return b, err
		}
	}
	return nil, nil
}

// pick returns the newest banner attached by join that reaches one of
// roles (or everyone).
func pick(ctx context.Context, q store.DBTX, join string, target entity.ID, roles []entity.ID) (*Banner, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.id, b.text, b.all_roles, b.created_at FROM banners b `+join+`
		ORDER BY b.created_at DESC, b.id DESC`, target)
	if err != nil {
		return nil, err
	}
	type cand struct {
		b   Banner
		all bool
	}
	var cands []cand
	for rows.Next() {
		var (
			c  cand
			at string
		)
		if err := rows.Scan(&c.b.ID, &c.b.Text, &c.all, &at); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range cands {
		if c.all {
			return &c.b, nil
		}
		for _, r := range roles {
			var n int
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM banner_roles WHERE banner_id = ? AND role_id = ?`,
				c.b.ID, r).Scan(&n); err != nil {
				return nil, err
			}
			if n > 0 {
				return &c.b, nil
			}
		}
	}
	return nil, nil
}

// chain returns id and its ancestors, nearest first, following parentQuery.
func chain(ctx context.Context, q store.DBTX, parentQuery string, id entity.ID) ([]entity.ID, error) {
	out := []entity.ID{id}
	for cur := id; len(out) < 64; {
		var parent entity.NullID
		err := q.QueryRowContext(ctx, parentQuery, cur).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !parent.Valid) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, parent.ID)
		cur = parent.ID
	}
	return out, nil
}

// Create stores a banner. Step 10 builds the admin screen over this; it
// exists now so the app's Home tab can be demonstrated.
func Create(ctx context.Context, q store.DBTX, text string, thisEventOnly, allRoles bool,
	roles, eventTypes, events []entity.ID, now time.Time) (entity.ID, error) {
	id := entity.NewID()
	if _, err := q.ExecContext(ctx, `INSERT INTO banners (id, text, this_event_only, all_roles, created_at)
		VALUES (?, ?, ?, ?, ?)`, id, text, thisEventOnly, allRoles, entity.FormatTime(now)); err != nil {
		return entity.Nil, err
	}
	for _, r := range roles {
		if _, err := q.ExecContext(ctx, `INSERT INTO banner_roles (banner_id, role_id) VALUES (?, ?)`, id, r); err != nil {
			return entity.Nil, err
		}
	}
	for _, t := range eventTypes {
		if _, err := q.ExecContext(ctx, `INSERT INTO banner_event_types (banner_id, event_type_id) VALUES (?, ?)`, id, t); err != nil {
			return entity.Nil, err
		}
	}
	for _, e := range events {
		if _, err := q.ExecContext(ctx, `INSERT INTO banner_events (banner_id, event_id) VALUES (?, ?)`, id, e); err != nil {
			return entity.Nil, err
		}
	}
	return id, nil
}
