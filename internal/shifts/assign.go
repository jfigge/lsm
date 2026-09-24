package shifts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lsm/internal/entity"
	"lsm/internal/staff"
	"lsm/internal/store"
)

var (
	// ErrNoRoster means the event's roster has not been committed: the
	// matcher consumes the capped roster, never raw signups.
	ErrNoRoster = errors.New("shifts: commit the event's roster before running the matcher")
	// ErrNotFound is an unknown event, post, person or assignment.
	ErrNotFound = errors.New("shifts: not found")
	// ErrWrongShift places someone on a post in a shift it is not staffed.
	ErrWrongShift = errors.New("shifts: that post is not staffed in that shift")
)

// MatchOptions tune a matcher run.
type MatchOptions struct {
	// PresentOnly draws from the people checked in and not checked out
	// instead of the roster: re-running on the night with who turned up.
	PresentOnly bool
}

// MatchSummary reports a run.
type MatchSummary struct {
	Placed int `json:"placed"`
	Pinned int `json:"pinned"` // kept from before
	Empty  int `json:"empty"`
	Pool   int `json:"pool"`
}

// RunMatcher drafts an event's assignments. Pinned placements (manual
// overrides, or matcher rows an admin pinned) are kept; every other draft
// row is replaced, so a re-run fills only unpinned slots.
func RunMatcher(ctx context.Context, db *sql.DB, eventID entity.ID, opt MatchOptions, by entity.ID, now time.Time) (MatchSummary, error) {
	var sum MatchSummary
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		in, err := loadMatchInput(ctx, tx, eventID, opt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM assignments
			WHERE event_id = ? AND pinned = 0 AND source = 'matcher' AND removed_at IS NULL`, eventID); err != nil {
			return err
		}
		res := Match(in)
		for _, p := range res.Placements {
			if _, err := tx.ExecContext(ctx, `INSERT INTO assignments
				(id, event_id, position_id, person_id, shift, source, pinned, needs_confirmation, created_at, created_by, reason)
				VALUES (?, ?, ?, ?, ?, 'matcher', 0, ?, ?, ?, ?)`,
				entity.NewID(), eventID, p.PostID, p.PersonID, p.Shift, p.NeedsConfirmation,
				entity.FormatTime(now), by, p.Reason); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE events SET matched_at = ? WHERE id = ?`, entity.FormatTime(now), eventID); err != nil {
			return err
		}
		sum = MatchSummary{Placed: len(res.Placements), Pinned: len(in.Pinned), Empty: len(res.Empty), Pool: len(in.People)}
		return nil
	})
	return sum, err
}

// eventTypes returns an event's type and its ancestors.
func eventTypes(ctx context.Context, q store.DBTX, eventID entity.ID) ([]any, time.Time, error) {
	e, err := EventByID(ctx, q, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE up(id, parent_id) AS (
			SELECT id, parent_id FROM event_types WHERE id = ?
			UNION ALL SELECT t.id, t.parent_id FROM event_types t JOIN up ON t.id = up.parent_id)
		SELECT id FROM up`, e.EventTypeID)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var id entity.ID
		if err := rows.Scan(&id); err != nil {
			return nil, time.Time{}, err
		}
		out = append(out, id)
	}
	return out, e.Date, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// applicablePosts lists the posts that apply to an event: linked to its
// type or any ancestor type.
func applicablePosts(ctx context.Context, q store.DBTX, eventID entity.ID) ([]MatchPost, error) {
	types, _, err := eventTypes(ctx, q, eventID)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name, p.area, p.department_id, p.shift_pattern, p.headcount,
			p.min_tenure_half_years, p.desirability, p.is_lead, COALESCE(p.pairing_rule, '')
		FROM positions p
		WHERE EXISTS (SELECT 1 FROM position_event_types pet
		              WHERE pet.position_id = p.id AND pet.event_type_id IN (`+placeholders(len(types))+`))
		ORDER BY p.area, p.name`, types...)
	if err != nil {
		return nil, err
	}
	var posts []MatchPost
	index := map[entity.ID]int{}
	for rows.Next() {
		var (
			p   MatchPost
			min sql.NullInt64
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Area, &p.Dept, &p.Pattern, &p.Headcount, &min, &p.Desirability,
			&p.IsLead, &p.Pairing); err != nil {
			rows.Close()
			return nil, err
		}
		if min.Valid {
			v := int(min.Int64)
			p.MinTenure = &v
		}
		index[p.ID] = len(posts)
		posts = append(posts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, query := range []struct {
		sql string
		add func(p *MatchPost, id entity.ID)
	}{
		{`SELECT position_id, capability_id FROM position_capabilities`, func(p *MatchPost, id entity.ID) { p.Caps = append(p.Caps, id) }},
		{`SELECT lead_position_id, position_id FROM position_lead_pool`, func(p *MatchPost, id entity.ID) { p.LeadOf = append(p.LeadOf, id) }},
	} {
		rows, err := q.QueryContext(ctx, query.sql)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var post, other entity.ID
			if err := rows.Scan(&post, &other); err != nil {
				rows.Close()
				return nil, err
			}
			if i, ok := index[post]; ok {
				query.add(&posts[i], other)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return posts, nil
}

func loadMatchInput(ctx context.Context, q store.DBTX, eventID entity.ID, opt MatchOptions) (MatchInput, error) {
	posts, err := applicablePosts(ctx, q, eventID)
	if err != nil {
		return MatchInput{}, err
	}
	_, date, err := eventTypes(ctx, q, eventID)
	if err != nil {
		return MatchInput{}, err
	}

	pool := `SELECT person_id FROM event_staff WHERE event_id = ?1`
	if opt.PresentOnly {
		pool = `SELECT person_id FROM checkins WHERE event_id = ?1 AND checked_out_at IS NULL`
	}
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE dept(role_id, top) AS (
			SELECT id, id FROM roles WHERE parent_id IS NULL
			UNION ALL SELECT r.id, dept.top FROM roles r JOIN dept ON r.parent_id = dept.role_id)
		SELECT p.id, p.name, p.badge, dept.top, p.hire_date, p.gender
		FROM persons p JOIN dept ON dept.role_id = p.role_id
		WHERE p.active = 1 AND p.id IN (`+pool+`)
		ORDER BY p.badge`, eventID)
	if err != nil {
		return MatchInput{}, err
	}
	var in MatchInput
	index := map[entity.ID]int{}
	for rows.Next() {
		var (
			p    MatchPerson
			hire string
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Badge, &p.Dept, &hire, &p.Gender); err != nil {
			rows.Close()
			return MatchInput{}, err
		}
		h, err := entity.ParseDate(hire)
		if err != nil {
			rows.Close()
			return MatchInput{}, err
		}
		p.Tenure = int(staff.Tenure(h, date))
		p.Caps, p.Queue = map[entity.ID]string{}, map[entity.ID]int{}
		index[p.ID] = len(in.People)
		in.People = append(in.People, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return MatchInput{}, err
	}
	if len(in.People) == 0 && !opt.PresentOnly {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_staff WHERE event_id = ?`, eventID).Scan(&n); err != nil {
			return MatchInput{}, err
		}
		if n == 0 {
			return MatchInput{}, ErrNoRoster
		}
	}

	rows, err = q.QueryContext(ctx, `SELECT person_id, capability_id, state FROM person_capability_effective
		WHERE state <> 'no_preference' AND person_id IN (`+pool+`)`, eventID)
	if err != nil {
		return MatchInput{}, err
	}
	for rows.Next() {
		var (
			pid, cid entity.ID
			state    string
		)
		if err := rows.Scan(&pid, &cid, &state); err != nil {
			rows.Close()
			return MatchInput{}, err
		}
		if i, ok := index[pid]; ok {
			in.People[i].Caps[cid] = state
		}
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT person_id, position_id, depth FROM position_queue WHERE person_id IN (`+pool+`)`, eventID)
	if err != nil {
		return MatchInput{}, err
	}
	for rows.Next() {
		var (
			pid, post entity.ID
			depth     int
		)
		if err := rows.Scan(&pid, &post, &depth); err != nil {
			rows.Close()
			return MatchInput{}, err
		}
		if i, ok := index[pid]; ok {
			in.People[i].Queue[post] = depth
		}
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT position_id, person_id, shift FROM assignments
		WHERE event_id = ? AND removed_at IS NULL AND pinned = 1`, eventID)
	if err != nil {
		return MatchInput{}, err
	}
	for rows.Next() {
		p := Placement{Pinned: true}
		if err := rows.Scan(&p.PostID, &p.PersonID, &p.Shift); err != nil {
			rows.Close()
			return MatchInput{}, err
		}
		in.Pinned = append(in.Pinned, p)
	}
	rows.Close()
	in.Posts = posts
	return in, rows.Err()
}

// ------------------------------------------------------------ overrides --

// Place puts a person on a post by hand. It is never refused: a post may
// hold more than its headcount, and min_tenure, a restriction, the lead
// default or the pairing rule may all be overridden — each override is
// recorded, with who and when, for the printed sheet. The person leaves
// any other post they held in that shift. Manual placements are pinned.
func Place(ctx context.Context, db *sql.DB, eventID, postID, personID entity.ID, shift string, by entity.ID, note string, now time.Time) error {
	return store.InTx(ctx, db, func(tx *sql.Tx) error {
		posts, err := applicablePosts(ctx, tx, eventID)
		if err != nil {
			return err
		}
		var post *MatchPost
		for i := range posts {
			if posts[i].ID == postID {
				post = &posts[i]
			}
		}
		if post == nil {
			return ErrNotFound
		}
		if (shift == ShiftSecond) != (post.Pattern == SecondShiftOnly) || (shift != ShiftFirst && shift != ShiftSecond) {
			return ErrWrongShift
		}
		in, err := loadMatchInput(ctx, tx, eventID, MatchOptions{})
		if err != nil && !errors.Is(err, ErrNoRoster) {
			return err
		}
		var person *MatchPerson
		for i := range in.People {
			if in.People[i].ID == personID {
				person = &in.People[i]
			}
		}
		if person == nil {
			// Someone off the roster can still be placed (a walk-up on the
			// night); load them directly.
			p, err := staff.PersonByID(ctx, tx, personID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			_, date, _ := eventTypes(ctx, tx, eventID)
			person = &MatchPerson{ID: p.ID, Name: p.Name, Tenure: int(staff.Tenure(p.HireDate, date)), Gender: string(p.Gender),
				Caps: map[entity.ID]string{}}
			rows, err := tx.QueryContext(ctx, `SELECT capability_id, state FROM person_capability_effective WHERE person_id = ?`, p.ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var (
					c entity.ID
					s string
				)
				if err := rows.Scan(&c, &s); err != nil {
					rows.Close()
					return err
				}
				person.Caps[c] = s
			}
			rows.Close()
		}

		if _, err := tx.ExecContext(ctx, `UPDATE assignments SET removed_at = ?, removed_by = ?
			WHERE event_id = ? AND person_id = ? AND shift = ? AND removed_at IS NULL`,
			entity.FormatTime(now), by, eventID, personID, shift); err != nil {
			return err
		}

		kinds := []string{"placement"}
		if post.MinTenure != nil && person.Tenure < *post.MinTenure {
			kinds = append(kinds, "min_tenure")
		}
		for _, c := range post.Caps {
			if person.Caps[c] == "restricted" {
				kinds = append(kinds, "restricted")
				break
			}
		}
		if post.IsLead {
			kinds = append(kinds, "lead")
		}
		confirm := false
		if post.Pairing == PairingMixedGender {
			mates, err := pairMates(ctx, tx, eventID, post, posts, personID)
			if err != nil {
				return err
			}
			for _, g := range mates {
				if g != "unknown" && g == person.Gender {
					kinds = append(kinds, "pairing")
				}
				if g == "unknown" || person.Gender == "unknown" {
					confirm = true
				}
			}
		}

		id := entity.NewID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO assignments
			(id, event_id, position_id, person_id, shift, source, pinned, needs_confirmation, created_at, created_by, reason)
			VALUES (?, ?, ?, ?, ?, 'manual', 1, ?, ?, ?, 'placed by hand')`,
			id, eventID, postID, personID, shift, confirm, entity.FormatTime(now), by); err != nil {
			return err
		}
		for _, k := range kinds {
			if _, err := tx.ExecContext(ctx, `INSERT INTO assignment_overrides (id, assignment_id, kind, by_person_id, at, note)
				VALUES (?, ?, ?, ?, ?, ?)`, entity.NewID(), id, k, by, entity.FormatTime(now), note); err != nil {
				return err
			}
		}
		return nil
	})
}

// pairMates returns the genders of the others on a post's pair.
func pairMates(ctx context.Context, q store.DBTX, eventID entity.ID, post *MatchPost, posts []MatchPost, except entity.ID) ([]string, error) {
	var ids []any
	for _, p := range posts {
		if p.Pairing == post.Pairing && p.Dept == post.Dept && pairKey(p.Name) == pairKey(post.Name) {
			ids = append(ids, p.ID)
		}
	}
	args := append([]any{eventID, except}, ids...)
	rows, err := q.QueryContext(ctx, `SELECT p.gender FROM assignments a JOIN persons p ON p.id = a.person_id
		WHERE a.event_id = ? AND a.person_id <> ? AND a.removed_at IS NULL AND a.shift = 'second'
		  AND a.position_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Unassign removes a placement. The row is kept as removed, so the
// night's history stays.
func Unassign(ctx context.Context, q store.DBTX, eventID, assignmentID, by entity.ID, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE assignments SET removed_at = ?, removed_by = ?
		WHERE id = ? AND event_id = ? AND removed_at IS NULL`, entity.FormatTime(now), by, assignmentID, eventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Pin keeps (or releases) a matcher placement across re-runs.
func Pin(ctx context.Context, q store.DBTX, eventID, assignmentID entity.ID, pinned bool) error {
	res, err := q.ExecContext(ctx, `UPDATE assignments SET pinned = ?
		WHERE id = ? AND event_id = ? AND removed_at IS NULL AND (source = 'matcher' OR ?)`,
		pinned, assignmentID, eventID, pinned)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Publish marks the assignment as published (the sheet goes to print), or
// back to draft.
func Publish(ctx context.Context, q store.DBTX, eventID entity.ID, published bool, by entity.ID, now time.Time) error {
	var at, who any
	if published {
		at, who = entity.FormatTime(now), by
	}
	res, err := q.ExecContext(ctx, `UPDATE events SET published_at = ?, published_by = ? WHERE id = ?`, at, who, eventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------- board --

// Override is a recorded human decision on an assignment.
type Override struct {
	Kind string `json:"kind"`
	By   string `json:"by"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
}

// Occupant is one person on a post.
type Occupant struct {
	AssignmentID      entity.ID  `json:"assignment_id"`
	PersonID          entity.ID  `json:"person_id"`
	Name              string     `json:"name"`
	Badge             string     `json:"badge"`
	Tenure            string     `json:"tenure_years"`
	Source            string     `json:"source"`
	Pinned            bool       `json:"pinned"`
	NeedsConfirmation bool       `json:"needs_confirmation"`
	Reason            string     `json:"reason"`
	Overrides         []Override `json:"overrides"`
	// Then is the person's post in the other shift: where a first-shift
	// door person goes at second shift, or where a second-shift person
	// came from.
	Then string `json:"then,omitempty"`
}

// BoardPost is one post with its occupants.
type BoardPost struct {
	PositionID entity.ID  `json:"position_id"`
	Name       string     `json:"name"`
	Area       string     `json:"area"`
	Pattern    string     `json:"shift_pattern"`
	Shift      string     `json:"shift"` // the shift it is staffed in
	Headcount  int        `json:"headcount"`
	IsLead     bool       `json:"is_lead"`
	Paired     bool       `json:"paired"`
	MinTenure  *int       `json:"min_tenure_half_years,omitempty"`
	Resources  []string   `json:"resources"`
	Occupants  []Occupant `json:"occupants"`
	// Fill is "0 of 1", "1 of 1", "2 of 1": the same notation for under,
	// exact and over, so a mismatch shows without shouting.
	Fill string `json:"fill"`
}

// PersonRef is a rostered person and where they are.
type PersonRef struct {
	ID     entity.ID `json:"id"`
	Name   string    `json:"name"`
	Badge  string    `json:"badge"`
	Tenure string    `json:"tenure_years"`
	First  string    `json:"first,omitempty"`
	Second string    `json:"second,omitempty"`
	// Freed is true when the first-shift post releases them at second
	// shift (so they need a second-shift post or a break duty).
	Freed bool `json:"freed"`
}

// BoardDept is one department's posts and people.
type BoardDept struct {
	DeptID entity.ID   `json:"department_id"`
	Name   string      `json:"department"`
	Posts  []BoardPost `json:"posts"`
	People []PersonRef `json:"people"`
	// Counts: established headcount against actual, per shift.
	FirstEstablished  int `json:"first_established"`
	FirstFilled       int `json:"first_filled"`
	SecondEstablished int `json:"second_established"`
	SecondFilled      int `json:"second_filled"`
	EmptyPosts        int `json:"empty_posts"`
	Unassigned        int `json:"unassigned"`
	NoSecondPost      int `json:"no_second_post"`
	Confirm           int `json:"needs_confirmation"`
}

// Board is an event's assignments, for review and for the printed sheet.
type Board struct {
	EventID     entity.ID   `json:"event_id"`
	Event       string      `json:"event"`
	Date        string      `json:"date"`
	MatchedAt   string      `json:"matched_at,omitempty"`
	PublishedAt string      `json:"published_at,omitempty"`
	RosterSize  int         `json:"roster_size"`
	Departments []BoardDept `json:"departments"`
}

// AssignmentBoard builds the board. Gender is never included: it is
// admin-only on the person record and never appears on a sheet.
func AssignmentBoard(ctx context.Context, q store.DBTX, eventID entity.ID, loc *time.Location) (Board, error) {
	e, err := EventByID(ctx, q, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return Board{}, ErrNotFound
	}
	if err != nil {
		return Board{}, err
	}
	b := Board{EventID: e.ID, Event: e.Name, Date: entity.FormatDate(e.Date)}
	var matched, published sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT matched_at, published_at, (SELECT COUNT(*) FROM event_staff WHERE event_id = ?)
		FROM events WHERE id = ?`, eventID, eventID).Scan(&matched, &published, &b.RosterSize); err != nil {
		return Board{}, err
	}
	stamp := func(s sql.NullString) string {
		if !s.Valid {
			return ""
		}
		t, _ := entity.ParseTime(s.String)
		return t.In(loc).Format("Mon 2 Jan 3:04 pm")
	}
	b.MatchedAt, b.PublishedAt = stamp(matched), stamp(published)

	posts, err := applicablePosts(ctx, q, eventID)
	if err != nil {
		return Board{}, err
	}
	postByID := map[entity.ID]*MatchPost{}
	for i := range posts {
		postByID[posts[i].ID] = &posts[i]
	}
	resources := map[entity.ID][]string{}
	rows, err := q.QueryContext(ctx, `SELECT pr.position_id, r.name FROM position_resources pr JOIN resources r ON r.id = pr.resource_id ORDER BY r.name`)
	if err != nil {
		return Board{}, err
	}
	for rows.Next() {
		var (
			id   entity.ID
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return Board{}, err
		}
		resources[id] = append(resources[id], name)
	}
	rows.Close()

	// Live assignments with people.
	type asg struct {
		Occupant
		post  entity.ID
		shift string
	}
	var all []asg
	rows, err = q.QueryContext(ctx, `SELECT a.id, a.position_id, a.shift, p.id, p.name, p.badge, p.hire_date,
			a.source, a.pinned, a.needs_confirmation, a.reason
		FROM assignments a JOIN persons p ON p.id = a.person_id
		WHERE a.event_id = ? AND a.removed_at IS NULL
		ORDER BY a.created_at, p.badge`, eventID)
	if err != nil {
		return Board{}, err
	}
	for rows.Next() {
		var (
			x    asg
			hire string
		)
		if err := rows.Scan(&x.AssignmentID, &x.post, &x.shift, &x.PersonID, &x.Name, &x.Badge, &hire,
			&x.Source, &x.Pinned, &x.NeedsConfirmation, &x.Reason); err != nil {
			rows.Close()
			return Board{}, err
		}
		h, _ := entity.ParseDate(hire)
		x.Tenure = staff.Tenure(h, e.Date).String()
		x.Overrides = []Override{}
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Board{}, err
	}
	overrides := map[entity.ID][]Override{}
	rows, err = q.QueryContext(ctx, `SELECT o.assignment_id, o.kind, COALESCE(p.name, ''), o.at, o.note
		FROM assignment_overrides o JOIN assignments a ON a.id = o.assignment_id
		LEFT JOIN persons p ON p.id = o.by_person_id
		WHERE a.event_id = ? AND a.removed_at IS NULL ORDER BY o.at`, eventID)
	if err != nil {
		return Board{}, err
	}
	for rows.Next() {
		var (
			id entity.ID
			o  Override
			at string
		)
		if err := rows.Scan(&id, &o.Kind, &o.By, &at, &o.Note); err != nil {
			rows.Close()
			return Board{}, err
		}
		t, _ := entity.ParseTime(at)
		o.At = t.In(loc).Format("2 Jan 3:04 pm")
		overrides[id] = append(overrides[id], o)
	}
	rows.Close()

	firstOf, secondOf := map[entity.ID]string{}, map[entity.ID]string{}
	for _, x := range all {
		name := x.post.String()
		if p := postByID[x.post]; p != nil {
			name = p.Name
		}
		if x.shift == ShiftFirst {
			firstOf[x.PersonID] = name
		} else if x.shift == ShiftSecond {
			secondOf[x.PersonID] = name
		}
	}

	// Departments and the roster.
	depts, err := topRoles(ctx, q)
	if err != nil {
		return Board{}, err
	}
	people := map[entity.ID][]PersonRef{}
	rows, err = q.QueryContext(ctx, `WITH RECURSIVE dept(role_id, top) AS (
			SELECT id, id FROM roles WHERE parent_id IS NULL
			UNION ALL SELECT r.id, dept.top FROM roles r JOIN dept ON r.parent_id = dept.role_id)
		SELECT p.id, p.name, p.badge, p.hire_date, dept.top
		FROM event_staff s JOIN persons p ON p.id = s.person_id JOIN dept ON dept.role_id = p.role_id
		WHERE s.event_id = ? ORDER BY p.name`, eventID)
	if err != nil {
		return Board{}, err
	}
	for rows.Next() {
		var (
			r    PersonRef
			hire string
			dept entity.ID
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Badge, &hire, &dept); err != nil {
			rows.Close()
			return Board{}, err
		}
		h, _ := entity.ParseDate(hire)
		r.Tenure = staff.Tenure(h, e.Date).String()
		r.First, r.Second = firstOf[r.ID], secondOf[r.ID]
		people[dept] = append(people[dept], r)
	}
	rows.Close()

	patternOf := map[string]ShiftPattern{}
	for _, p := range posts {
		patternOf[p.Name] = p.Pattern
	}
	for _, d := range depts {
		bd := BoardDept{DeptID: d.ID, Name: d.Name, Posts: []BoardPost{}, People: people[d.ID]}
		if bd.People == nil {
			bd.People = []PersonRef{}
		}
		for _, p := range posts {
			if p.Dept != d.ID {
				continue
			}
			shift := ShiftFirst
			if p.Pattern == SecondShiftOnly {
				shift = ShiftSecond
			}
			bp := BoardPost{PositionID: p.ID, Name: p.Name, Area: p.Area, Pattern: string(p.Pattern), Shift: shift,
				Headcount: p.Headcount, IsLead: p.IsLead, Paired: p.Pairing != "", MinTenure: p.MinTenure,
				Resources: resources[p.ID], Occupants: []Occupant{}}
			if bp.Resources == nil {
				bp.Resources = []string{}
			}
			for _, x := range all {
				if x.post != p.ID || x.shift != shift {
					continue
				}
				o := x.Occupant
				if ov := overrides[o.AssignmentID]; ov != nil {
					o.Overrides = ov
				}
				if shift == ShiftFirst {
					o.Then = secondOf[o.PersonID]
				} else {
					o.Then = firstOf[o.PersonID]
				}
				if o.NeedsConfirmation {
					bd.Confirm++
				}
				bp.Occupants = append(bp.Occupants, o)
			}
			bp.Fill = fmt.Sprintf("%d of %d", len(bp.Occupants), p.Headcount)
			if shift == ShiftFirst {
				bd.FirstEstablished += p.Headcount
				bd.FirstFilled += min(len(bp.Occupants), p.Headcount)
			} else {
				bd.SecondEstablished += p.Headcount
				bd.SecondFilled += min(len(bp.Occupants), p.Headcount)
			}
			if len(bp.Occupants) < p.Headcount {
				bd.EmptyPosts += p.Headcount - len(bp.Occupants)
			}
			bd.Posts = append(bd.Posts, bp)
		}
		for i := range bd.People {
			r := &bd.People[i]
			if r.First == "" {
				bd.Unassigned++
				continue
			}
			if pat := patternOf[r.First]; pat == TwoShiftDoor || pat == FirstShiftOnly {
				r.Freed = true
				if r.Second == "" {
					bd.NoSecondPost++
				}
			}
		}
		if len(bd.Posts) == 0 && len(bd.People) == 0 {
			continue
		}
		sort.SliceStable(bd.Posts, func(i, j int) bool {
			if bd.Posts[i].Shift != bd.Posts[j].Shift {
				return bd.Posts[i].Shift == ShiftFirst
			}
			return false
		})
		b.Departments = append(b.Departments, bd)
	}
	return b, nil
}

type role struct {
	ID   entity.ID
	Name string
}

func topRoles(ctx context.Context, q store.DBTX) ([]role, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM roles WHERE parent_id IS NULL ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []role
	for rows.Next() {
		var r role
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
