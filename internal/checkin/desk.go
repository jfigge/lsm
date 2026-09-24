package checkin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"lsm/internal/banner"
	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

var (
	// ErrBadgeUnknown is a scan that matches nobody. It is reported
	// distinctly from "not scheduled tonight": a different problem on the
	// night, and reception has to tell them apart (SPEC §7).
	ErrBadgeUnknown = errors.New("checkin: badge not recognised")
	// ErrNotFound is an unknown event, person or issue.
	ErrNotFound = errors.New("checkin: not found")
	// ErrNotCheckedIn is a check-out for someone who never checked in.
	ErrNotCheckedIn = errors.New("checkin: not checked in")
	// ErrNoneAvailable is "first available" with every instance out.
	ErrNoneAvailable = errors.New("checkin: none available")
)

// NormalizeBadge cleans a scan. The badge symbology and payload length
// are not yet known (SPEC §7), so this only trims and rejects what no
// barcode would carry; lookup is by exact match.
func NormalizeBadge(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false
		}
	}
	return s, true
}

// PersonCard identifies someone at the desk.
type PersonCard struct {
	ID         entity.ID `json:"id"`
	Name       string    `json:"name"`
	Badge      string    `json:"badge"`
	Initials   string    `json:"initials"`
	Photo      string    `json:"photo,omitempty"`
	Department string    `json:"department"`
}

// Post is the person's initial assignment for the night.
type Post struct {
	PositionID entity.ID `json:"position_id"`
	Name       string    `json:"name"`
	Area       string    `json:"area"`
	Floor      string    `json:"floor,omitempty"`
}

// Visit is a check-in record in display form.
type Visit struct {
	ID           entity.ID  `json:"id"`
	CheckedInAt  time.Time  `json:"checked_in_at"`
	CheckedIn    string     `json:"checked_in"` // "6:42 pm"
	InLocation   string     `json:"in_location"`
	InStation    string     `json:"in_station,omitempty"`
	CheckedOutAt *time.Time `json:"checked_out_at,omitempty"`
	CheckedOut   string     `json:"checked_out,omitempty"`
	OutLocation  string     `json:"out_location,omitempty"`
}

// Issued is one resource handed out on this visit.
type Issued struct {
	ID         entity.ID  `json:"id"`
	ResourceID entity.ID  `json:"resource_id"`
	Resource   string     `json:"resource"`
	Number     string     `json:"number,omitempty"`
	Registered bool       `json:"registered"` // a number on the rack, not free-typed
	IssuedAt   time.Time  `json:"issued_at"`
	Issued     string     `json:"issued"`
	IssuedBy   string     `json:"issued_by,omitempty"`
	ReturnedAt *time.Time `json:"returned_at,omitempty"`
	Returned   string     `json:"returned,omitempty"`
}

// ResourceField is one resource on the desk: required by the post or
// available to issue ad hoc. The form is derived from this, so a radio is
// a configured resource, not a hardcoded column.
type ResourceField struct {
	ID       entity.ID `json:"id"`
	Name     string    `json:"name"`
	Tracked  bool      `json:"tracked"`
	Required bool      `json:"required"`
	// Available lists the registered numbers not currently out, in rack
	// order; issued instances drop off it.
	Available []string `json:"available,omitempty"`
}

// Desk is everything reception needs about one person for one event.
type Desk struct {
	EventID  entity.ID      `json:"event_id"`
	Event    string         `json:"event"`
	Person   PersonCard     `json:"person"`
	Rostered bool           `json:"rostered"`
	Post     *Post          `json:"post"`
	Banner   *banner.Banner `json:"banner"`
	Visit    *Visit         `json:"visit"`
	Issued   []Issued       `json:"issued"`
	// Fields are the post's required resources first, then the rest.
	Fields []ResourceField `json:"fields"`
	// Notices are plain statements for the operator ("Not on tonight's
	// roster").
	Notices []string `json:"notices"`
}

// ResolveBadge maps a scanned badge to a person.
func ResolveBadge(ctx context.Context, q store.DBTX, raw string) (entity.ID, error) {
	badge, ok := NormalizeBadge(raw)
	if !ok {
		return entity.Nil, ErrBadgeUnknown
	}
	var id entity.ID
	err := q.QueryRowContext(ctx, `SELECT id FROM persons WHERE badge = ? AND active = 1`, badge).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Nil, ErrBadgeUnknown
	}
	return id, err
}

// SearchPeople is the manual lookup for a badge that will not scan or was
// left at home: by name (any word) or badge prefix.
func SearchPeople(ctx context.Context, q store.DBTX, query string, limit int) ([]PersonCard, error) {
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return []PersonCard{}, nil
	}
	escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query)
	rows, err := q.QueryContext(ctx, `SELECT id FROM persons
		WHERE active = 1 AND (name LIKE ? ESCAPE '\' OR badge LIKE ? ESCAPE '\')
		ORDER BY name LIMIT ?`, "%"+escaped+"%", escaped+"%", limit)
	if err != nil {
		return nil, err
	}
	var ids []entity.ID
	for rows.Next() {
		var id entity.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PersonCard, 0, len(ids))
	for _, id := range ids {
		c, err := card(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func card(ctx context.Context, q store.DBTX, personID entity.ID) (PersonCard, error) {
	p, err := staff.PersonByID(ctx, q, personID)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonCard{}, ErrNotFound
	}
	if err != nil {
		return PersonCard{}, err
	}
	var dept string
	err = q.QueryRowContext(ctx, `WITH RECURSIVE up(id, parent_id, name) AS (
			SELECT id, parent_id, name FROM roles WHERE id = ?
			UNION ALL SELECT r.id, r.parent_id, r.name FROM roles r JOIN up ON r.id = up.parent_id)
		SELECT name FROM up WHERE parent_id IS NULL`, p.RoleID).Scan(&dept)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PersonCard{}, err
	}
	return PersonCard{ID: p.ID, Name: p.Name, Badge: p.Badge, Initials: initials(p.Name), Photo: p.Photo, Department: dept}, nil
}

func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		out = append(out, unicode.ToUpper([]rune(w)[0]))
	}
	if len(out) > 2 {
		out = []rune{out[0], out[len(out)-1]}
	}
	return string(out)
}

// DeskFor builds the reception view of a person for an event.
func DeskFor(ctx context.Context, q store.DBTX, eventID, personID entity.ID, loc *time.Location) (Desk, error) {
	e, err := shifts.EventByID(ctx, q, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return Desk{}, ErrNotFound
	}
	if err != nil {
		return Desk{}, err
	}
	c, err := card(ctx, q, personID)
	if err != nil {
		return Desk{}, err
	}
	d := Desk{EventID: e.ID, Event: e.Name, Person: c, Issued: []Issued{}, Notices: []string{}}

	var rosterSize int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(person_id = ?), 0) FROM event_staff WHERE event_id = ?`,
		personID, eventID).Scan(&rosterSize, &d.Rostered); err != nil {
		return Desk{}, err
	}
	if d.Post, err = initialPost(ctx, q, eventID, personID); err != nil {
		return Desk{}, err
	}
	switch {
	case rosterSize > 0 && !d.Rostered && d.Post == nil:
		d.Notices = append(d.Notices, "Not on tonight's roster")
	case d.Post == nil:
		d.Notices = append(d.Notices, "No post assigned yet")
	}

	var roleID entity.ID
	if err := q.QueryRowContext(ctx, `SELECT role_id FROM persons WHERE id = ?`, personID).Scan(&roleID); err != nil {
		return Desk{}, err
	}
	if d.Banner, err = banner.For(ctx, q, roleID, eventID); err != nil {
		return Desk{}, err
	}
	if d.Visit, err = visit(ctx, q, eventID, personID, loc); err != nil {
		return Desk{}, err
	}
	if d.Visit != nil {
		if d.Issued, err = issuedOn(ctx, q, d.Visit.ID, loc); err != nil {
			return Desk{}, err
		}
	}
	if d.Fields, err = fields(ctx, q, d.Post); err != nil {
		return Desk{}, err
	}
	return d, nil
}

// initialPost is the person's first-shift (or whole-session) post.
func initialPost(ctx context.Context, q store.DBTX, eventID, personID entity.ID) (*Post, error) {
	var p Post
	err := q.QueryRowContext(ctx, `SELECT pos.id, pos.name, pos.area, pos.floor
		FROM assignments a JOIN positions pos ON pos.id = a.position_id
		WHERE a.event_id = ? AND a.person_id = ? AND a.removed_at IS NULL
		ORDER BY CASE a.shift WHEN 'first' THEN 0 WHEN 'second' THEN 1 ELSE 2 END, a.created_at
		LIMIT 1`, eventID, personID).Scan(&p.PositionID, &p.Name, &p.Area, &p.Floor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func visit(ctx context.Context, q store.DBTX, eventID, personID entity.ID, loc *time.Location) (*Visit, error) {
	var (
		v           Visit
		in          string
		out, outLoc sql.NullString
		inStation   sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT c.id, c.checked_in_at, c.checked_in_location, s.name,
			c.checked_out_at, c.checked_out_location
		FROM checkins c LEFT JOIN stations s ON s.id = c.checked_in_station
		WHERE c.event_id = ? AND c.person_id = ?`, eventID, personID).
		Scan(&v.ID, &in, &v.InLocation, &inStation, &out, &outLoc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.CheckedInAt, _ = entity.ParseTime(in)
	v.CheckedIn = clock(v.CheckedInAt, loc)
	v.InStation = inStation.String
	if out.Valid {
		t, _ := entity.ParseTime(out.String)
		v.CheckedOutAt, v.CheckedOut, v.OutLocation = &t, clock(t, loc), outLoc.String
	}
	return &v, nil
}

func issuedOn(ctx context.Context, q store.DBTX, checkinID entity.ID, loc *time.Location) ([]Issued, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.id, r.id, r.name, COALESCE(i.instance_number, ''), i.instance_id IS NOT NULL,
			i.issued_at, COALESCE(b.name, ''), i.returned_at
		FROM resource_issues i
		JOIN resources r ON r.id = i.resource_id
		LEFT JOIN persons b ON b.id = i.issued_by
		WHERE i.checkin_id = ?
		ORDER BY i.returned_at IS NOT NULL, r.name, i.issued_at`, checkinID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issued{}
	for rows.Next() {
		var (
			x        Issued
			at       string
			returned sql.NullString
		)
		if err := rows.Scan(&x.ID, &x.ResourceID, &x.Resource, &x.Number, &x.Registered, &at, &x.IssuedBy, &returned); err != nil {
			return nil, err
		}
		x.IssuedAt, _ = entity.ParseTime(at)
		x.Issued = clock(x.IssuedAt, loc)
		if returned.Valid {
			t, _ := entity.ParseTime(returned.String)
			x.ReturnedAt, x.Returned = &t, clock(t, loc)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// fields lists every resource, the post's required ones first, with the
// registered numbers currently available for tracked ones.
func fields(ctx context.Context, q store.DBTX, post *Post) ([]ResourceField, error) {
	required := map[entity.ID]bool{}
	if post != nil {
		rows, err := q.QueryContext(ctx, `SELECT resource_id FROM position_resources WHERE position_id = ?`, post.PositionID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id entity.ID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			required[id] = true
		}
		rows.Close()
	}
	resources, err := ListResources(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceField, 0, len(resources))
	for _, r := range resources {
		f := ResourceField{ID: r.ID, Name: r.Name, Tracked: r.Tracked, Required: required[r.ID]}
		if r.Tracked {
			if f.Available, err = Available(ctx, q, r.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Required && !out[j].Required })
	return out, nil
}

// Available returns a tracked resource's registered, unretired numbers
// that are not currently out — on any night: a radio never returned from
// an earlier event is still out.
func Available(ctx context.Context, q store.DBTX, resourceID entity.ID) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT ri.number FROM resource_instances ri
		WHERE ri.resource_id = ?1 AND ri.retired = 0 AND NOT EXISTS (
			SELECT 1 FROM resource_issues i
			WHERE i.resource_id = ?1 AND i.returned_at IS NULL
			  AND (i.instance_id = ri.id OR i.instance_number = ri.number))
		ORDER BY length(ri.number), ri.number`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Where records how a check-in or check-out happened.
type Where struct {
	Location string        // "kiosk" or "reception"
	By       entity.NullID // the reception operator
	Station  entity.NullID // the kiosk
}

// CheckIn records arrival. It is idempotent: someone already checked in
// keeps their original time and place, and created reports false.
func CheckIn(ctx context.Context, q store.DBTX, eventID, personID entity.ID, w Where, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO checkins
		(id, event_id, person_id, checked_in_at, checked_in_location, checked_in_by, checked_in_station)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (event_id, person_id) DO NOTHING`,
		entity.NewID(), eventID, personID, entity.FormatTime(now), w.Location, w.By, w.Station)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CheckOut records departure. Unreturned items do not block it: the desk
// shows them so return can be confirmed, and the end-of-night report
// catches anything missed.
func CheckOut(ctx context.Context, q store.DBTX, eventID, personID entity.ID, w Where, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE checkins SET checked_out_at = ?, checked_out_location = ?,
			checked_out_by = ?, checked_out_station = ?
		WHERE event_id = ? AND person_id = ? AND checked_out_at IS NULL`,
		entity.FormatTime(now), w.Location, w.By, w.Station, eventID, personID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var out sql.NullString
		err := q.QueryRowContext(ctx, `SELECT checked_out_at FROM checkins WHERE event_id = ? AND person_id = ?`,
			eventID, personID).Scan(&out)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotCheckedIn
		}
		return err // already checked out: nothing to do
	}
	return nil
}

// IssueRequest asks for one resource to be issued.
type IssueRequest struct {
	ResourceID entity.ID `json:"resource_id"`
	// Number is a specific or free-typed instance number; empty with
	// FirstAvailable picks the lowest available registered number.
	Number         string `json:"number"`
	FirstAvailable bool   `json:"first_available"`
}

// Issue hands a resource to a person, checking them in first if they
// are not yet (reception checks in and issues in one action). A number
// is never refused: an unregistered one is recorded as typed, and one
// the records say is already out is issued with a warning.
func Issue(ctx context.Context, db *sql.DB, eventID, personID entity.ID, req IssueRequest, by entity.ID,
	now time.Time) (warning string, err error) {
	err = store.InTx(ctx, db, func(tx *sql.Tx) error {
		var r Resource
		err := tx.QueryRowContext(ctx, `SELECT id, name, description, tracked FROM resources WHERE id = ?`, req.ResourceID).
			Scan(&r.ID, &r.Name, &r.Description, &r.Tracked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := CheckIn(ctx, tx, eventID, personID, Where{Location: "reception", By: entity.Some(by)}, now); err != nil {
			return err
		}
		var checkinID entity.ID
		if err := tx.QueryRowContext(ctx, `SELECT id FROM checkins WHERE event_id = ? AND person_id = ?`,
			eventID, personID).Scan(&checkinID); err != nil {
			return err
		}

		number := strings.TrimSpace(req.Number)
		var instance entity.NullID
		if r.Tracked {
			if number == "" {
				if !req.FirstAvailable {
					return fmt.Errorf("%w: choose a %s number", ErrInput, r.Name)
				}
				avail, err := Available(ctx, tx, r.ID)
				if err != nil {
					return err
				}
				if len(avail) == 0 {
					return fmt.Errorf("%w: no registered %s is free — type the number instead", ErrNoneAvailable, r.Name)
				}
				number = avail[0]
			}
			var id entity.ID
			err := tx.QueryRowContext(ctx, `SELECT id FROM resource_instances WHERE resource_id = ? AND number = ?`,
				r.ID, number).Scan(&id)
			switch {
			case err == nil:
				instance = entity.Some(id)
			case errors.Is(err, sql.ErrNoRows):
				warning = fmt.Sprintf("%s %s is not on the register; recorded as typed", r.Name, number)
			default:
				return err
			}
			var holder string
			err = tx.QueryRowContext(ctx, `SELECT p.name FROM resource_issues i
				JOIN checkins c ON c.id = i.checkin_id JOIN persons p ON p.id = c.person_id
				WHERE i.resource_id = ? AND i.instance_number = ? AND i.returned_at IS NULL LIMIT 1`,
				r.ID, number).Scan(&holder)
			if err == nil {
				warning = fmt.Sprintf("%s %s is still recorded as issued to %s", r.Name, number, holder)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		} else {
			number = ""
		}
		var num sql.NullString
		if number != "" {
			num = sql.NullString{String: number, Valid: true}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO resource_issues
			(id, checkin_id, resource_id, instance_id, instance_number, issued_at, issued_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			entity.NewID(), checkinID, r.ID, instance, num, entity.FormatTime(now), by)
		return err
	})
	return warning, err
}

// ErrInput is a request the operator got wrong.
var ErrInput = errors.New("checkin: invalid request")

// Return records an issued item coming back. The issue must belong to
// this person's visit for this event.
func Return(ctx context.Context, q store.DBTX, eventID, personID, issueID, by entity.ID, now time.Time) error {
	const visit = `(SELECT id FROM checkins WHERE event_id = ? AND person_id = ?)`
	res, err := q.ExecContext(ctx, `UPDATE resource_issues SET returned_at = ?, returned_by = ?
		WHERE id = ? AND returned_at IS NULL AND checkin_id = `+visit,
		entity.FormatTime(now), by, issueID, eventID, personID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var x int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM resource_issues WHERE id = ? AND checkin_id = `+visit,
			issueID, eventID, personID).Scan(&x)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err // already returned: nothing to do
	}
	return nil
}

func clock(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("3:04 pm")
}
