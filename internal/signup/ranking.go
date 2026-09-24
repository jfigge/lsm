package signup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

// Tuning constants for the rules. The weights are admin data; these shape
// what each rule's 0..100 means and are documented in RuleDescriptions.
const (
	// SeasonMonths is the look-back window for availability rate, ratings,
	// season load and recency: the twelve months up to the event.
	SeasonMonths = 12
	// RecencyFullDays is the gap since last worked that scores 100.
	RecencyFullDays = 30
	// UnratedScore is the performance score of a person with no ratings
	// this season: acceptable, the slider's default.
	UnratedScore = 50
)

var ratingScore = map[staff.Rating]float64{
	staff.RatingExceptional:     100,
	staff.RatingAboveAverage:    75,
	staff.RatingAcceptable:      50,
	staff.RatingBelowPar:        25,
	staff.RatingNeedsDiscipline: 0,
}

// RuleScore is one rule's contribution to a person's rank: the explanation
// shown per person.
type RuleScore struct {
	Rule   string  `json:"rule"`
	Weight int     `json:"weight"`
	Score  float64 `json:"score"`  // 0..100 on the rule's own terms
	Points float64 `json:"points"` // weight × score / 100, summed into the total
	Detail string  `json:"detail"`
}

// Signup is what the person offered for the event.
type Signup struct {
	Status     shifts.AvailabilityStatus `json:"status"`
	WindowID   *entity.ID                `json:"window_id"`
	SignedUpAt time.Time                 `json:"signed_up_at"`
}

// Override records an admin pulling a person above the line.
type Override struct {
	ByID   entity.NullID `json:"by_id"`
	ByName string        `json:"by_name"`
	At     time.Time     `json:"at"`
	Note   string        `json:"note"`
}

// Candidate is one ranked signup.
type Candidate struct {
	PersonID entity.ID   `json:"person_id"`
	Name     string      `json:"name"`
	Badge    string      `json:"badge"`
	Signup   Signup      `json:"signup"`
	Rank     int         `json:"rank"` // 1-based position in the final order
	Total    float64     `json:"total"`
	Scores   []RuleScore `json:"scores"`
	// AboveLine is the recommendation: on the roster for the night.
	AboveLine bool `json:"above_line"`
	// OnRoster is whether the person is on the committed roster, which
	// can lag the recommendation until it is committed again.
	OnRoster bool      `json:"on_roster"`
	Override *Override `json:"override,omitempty"`
	// Displaced marks someone the ranking put above the line who was
	// pushed below it by an override.
	Displaced bool `json:"displaced,omitempty"`
}

// Requirement is a department's required headcount for an event.
type Requirement struct {
	// Headcount is nil when nothing is required: the department has no
	// posts for the event and no override, so every signup is kept.
	Headcount *int `json:"headcount"`
	// Source is "event", "event_type:<name>", "posts" or "none".
	Source string `json:"source"`
	// Posts is the headcount derived from the department's posts, shown
	// beside an override so the gap between the two stays visible.
	Posts int `json:"posts"`
}

// DepartmentRanking ranks one department's signups for an event against
// its required headcount.
type DepartmentRanking struct {
	DepartmentID entity.ID   `json:"department_id"`
	Department   string      `json:"department"`
	Required     Requirement `json:"required"`
	Signups      int         `json:"signups"`
	AboveLine    int         `json:"above_line"`
	// Shortfall is how many more people the requirement calls for than
	// signed up; 0 when covered.
	Shortfall  int         `json:"shortfall"`
	Candidates []Candidate `json:"candidates"`
}

// EventRanking is the full capped roster recommendation for an event.
type EventRanking struct {
	EventID     entity.ID `json:"event_id"`
	Event       string    `json:"event"`
	Date        string    `json:"date"`
	Weights     Weights   `json:"weights"`
	Expectation float64   `json:"expectation"`
	// RosterSize is how many people the committed roster holds; 0 means
	// the recommendation has not been committed.
	RosterSize  int                 `json:"roster_size"`
	Departments []DepartmentRanking `json:"departments"`
}

// RankEvent ranks an event's signups with the given weights.
func RankEvent(ctx context.Context, q store.DBTX, eventID entity.ID, w Weights) (EventRanking, error) {
	d, err := loadEvent(ctx, q, eventID)
	if err != nil {
		return EventRanking{}, err
	}
	return d.rank(w), nil
}

// ------------------------------------------------------------------ data --

// eventData is everything the rules need for one event, loaded once so a
// preview can rank it under two sets of weights.
type eventData struct {
	event       shifts.Event
	expectation float64
	heldCount   int // season events before this one: season load's denominator
	depts       []dept
	people      map[entity.ID]*person
	roster      map[entity.ID]bool
}

type dept struct {
	id       entity.ID
	name     string
	required Requirement
	need     map[entity.ID]int // capability → posts needing it
	capNames map[entity.ID]string
	members  []entity.ID // candidates, in signup order
}

type person struct {
	id       entity.ID
	name     string
	badge    string
	dept     entity.ID
	hire     time.Time
	signup   Signup
	holds    map[entity.ID]bool // capabilities held as an effective "yes"
	override *Override

	rateAvailable, rateEvents int
	ratings                   []staff.Rating
	worked                    int
	lastWorked                time.Time
}

func loadEvent(ctx context.Context, q store.DBTX, eventID entity.ID) (*eventData, error) {
	e, err := shifts.EventByID(ctx, q, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &eventData{event: e, people: map[entity.ID]*person{}}
	if d.expectation, err = Expectation(ctx, q); err != nil {
		return nil, err
	}

	roles, err := staff.ListRoles(ctx, q)
	if err != nil {
		return nil, err
	}
	deptOf := Departments(roles)
	types, err := eventTypeChain(ctx, q, e.EventTypeID)
	if err != nil {
		return nil, err
	}

	// Candidates: everyone offering themselves for the event.
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name, p.badge, p.role_id, p.hire_date,
			a.status, a.window_id, a.signed_up_at
		FROM availability a JOIN persons p ON p.id = a.person_id
		WHERE a.event_id = ? AND a.status IN ('all_shifts', 'window') AND p.active = 1
		ORDER BY a.signed_up_at, p.badge`, eventID)
	if err != nil {
		return nil, err
	}
	byDept := map[entity.ID][]entity.ID{}
	for rows.Next() {
		var (
			p              person
			role           entity.ID
			hire, signedAt string
			win            entity.NullID
		)
		if err := rows.Scan(&p.id, &p.name, &p.badge, &role, &hire, &p.signup.Status, &win, &signedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if p.hire, err = entity.ParseDate(hire); err != nil {
			rows.Close()
			return nil, err
		}
		if p.signup.SignedUpAt, err = entity.ParseTime(signedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if win.Valid {
			p.signup.WindowID = &win.ID
		}
		p.dept = deptOf[role].ID
		p.holds = map[entity.ID]bool{}
		d.people[p.id] = &p
		byDept[p.dept] = append(byDept[p.dept], p.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Departments: every top-level role, kept if it has signups or a
	// requirement (a requirement with no signups is a visible shortfall).
	for _, r := range roles {
		if r.ParentID.Valid {
			continue
		}
		dp := dept{id: r.ID, name: r.Name, members: byDept[r.ID]}
		if dp.required, dp.need, dp.capNames, err = requirement(ctx, q, r.ID, entity.Some(e.ID), types); err != nil {
			return nil, err
		}
		if len(dp.members) > 0 || (dp.required.Headcount != nil && *dp.required.Headcount > 0) {
			d.depts = append(d.depts, dp)
		}
	}

	if err := d.loadHistory(ctx, q); err != nil {
		return nil, err
	}
	if d.heldCount, err = d.heldBefore(ctx, q); err != nil {
		return nil, err
	}
	if d.roster, err = rosterOf(ctx, q, eventID); err != nil {
		return nil, err
	}
	if err := d.loadCapabilities(ctx, q); err != nil {
		return nil, err
	}
	return d, d.loadOverrides(ctx, q)
}

// Departments maps every role to its top-level ancestor, the department
// whose signups it competes with.
func Departments(roles []staff.Role) map[entity.ID]staff.Role {
	byID := map[entity.ID]staff.Role{}
	for _, r := range roles {
		byID[r.ID] = r
	}
	out := map[entity.ID]staff.Role{}
	for _, r := range roles {
		top := r
		for i := 0; top.ParentID.Valid && i < len(roles); i++ {
			top = byID[top.ParentID.ID]
		}
		out[r.ID] = top
	}
	return out
}

// eventTypeChain returns the type and its ancestors, nearest first.
func eventTypeChain(ctx context.Context, q store.DBTX, typeID entity.ID) ([]shifts.EventType, error) {
	all, err := shifts.ListEventTypes(ctx, q)
	if err != nil {
		return nil, err
	}
	byID := map[entity.ID]shifts.EventType{}
	for _, t := range all {
		byID[t.ID] = t
	}
	var chain []shifts.EventType
	for cur, ok := byID[typeID]; ok && len(chain) <= len(all); cur, ok = byID[cur.ParentID.ID] {
		chain = append(chain, cur)
		if !cur.ParentID.Valid {
			break
		}
	}
	return chain, nil
}

// requirement resolves a department's required headcount for an event:
// an event override, else the nearest event-type override, else the
// department's posts that apply to the event. Second-shift-only posts are
// excluded from the derived count because they are filled by people
// redeploying from first shift. It also returns the event's capability
// needs for the department (all applicable posts, every shift).
func requirement(ctx context.Context, q store.DBTX, deptID entity.ID, eventID entity.NullID,
	types []shifts.EventType) (Requirement, map[entity.ID]int, map[entity.ID]string, error) {
	need := map[entity.ID]int{}
	names := map[entity.ID]string{}
	typeIDs := make([]any, 0, len(types))
	placeholders := ""
	for i, t := range types {
		typeIDs = append(typeIDs, t.ID)
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
	}
	if len(types) == 0 {
		return Requirement{Source: "none"}, need, names, nil
	}

	var posts int
	args := append([]any{deptID}, typeIDs...)
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(headcount), 0) FROM positions p
		WHERE p.department_id = ? AND p.shift_pattern <> 'second_shift_only'
		  AND EXISTS (SELECT 1 FROM position_event_types pet
		              WHERE pet.position_id = p.id AND pet.event_type_id IN (`+placeholders+`))`,
		args...).Scan(&posts)
	if err != nil {
		return Requirement{}, nil, nil, err
	}

	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name, SUM(p.headcount) FROM positions p
		JOIN position_capabilities pc ON pc.position_id = p.id
		JOIN capabilities c ON c.id = pc.capability_id
		WHERE p.department_id = ?
		  AND EXISTS (SELECT 1 FROM position_event_types pet
		              WHERE pet.position_id = p.id AND pet.event_type_id IN (`+placeholders+`))
		GROUP BY c.id, c.name`, args...)
	if err != nil {
		return Requirement{}, nil, nil, err
	}
	for rows.Next() {
		var (
			id   entity.ID
			name string
			n    int
		)
		if err := rows.Scan(&id, &name, &n); err != nil {
			rows.Close()
			return Requirement{}, nil, nil, err
		}
		need[id], names[id] = n, name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Requirement{}, nil, nil, err
	}

	var n int
	if eventID.Valid {
		err = q.QueryRowContext(ctx, `SELECT headcount FROM headcount_overrides WHERE role_id = ? AND event_id = ?`,
			deptID, eventID.ID).Scan(&n)
		if err == nil {
			return Requirement{Headcount: &n, Source: "event", Posts: posts}, need, names, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Requirement{}, nil, nil, err
		}
	}
	for _, t := range types {
		err := q.QueryRowContext(ctx, `SELECT headcount FROM headcount_overrides WHERE role_id = ? AND event_type_id = ?`,
			deptID, t.ID).Scan(&n)
		if err == nil {
			return Requirement{Headcount: &n, Source: "event_type:" + t.Name, Posts: posts}, need, names, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Requirement{}, nil, nil, err
		}
	}
	if posts > 0 {
		return Requirement{Headcount: &posts, Source: "posts", Posts: posts}, need, names, nil
	}
	return Requirement{Source: "none"}, need, names, nil
}

// loadHistory gathers the season's availability, ratings and work for
// the candidates. Every query is restricted to the candidates and the
// season's events up front; ranking runs on every personal view, so it
// has to stay cheap.
func (d *eventData) loadHistory(ctx context.Context, q store.DBTX) error {
	if len(d.people) == 0 {
		return nil
	}
	from := entity.FormatDate(d.event.Date.AddDate(0, -SeasonMonths, 0))
	on := entity.FormatDate(d.event.Date)
	monthEnd := entity.FormatDate(time.Date(d.event.Date.Year(), d.event.Date.Month()+1, 1, 0, 0, 0, 0, time.UTC))
	const candidates = `SELECT person_id FROM availability WHERE event_id = ? AND status IN ('all_shifts', 'window')`

	// Availability rate over opened months of the season, through the
	// event's own month. In a closed month every event counts and an
	// unanswered one is unavailable; in a still-open month only answered
	// events count.
	var closedEvents int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e
		JOIN schedule_months sm ON sm.month = substr(e.event_date, 1, 7) AND sm.status = 'closed'
		WHERE e.event_date > ? AND e.event_date < ?`, from, monthEnd).Scan(&closedEvents); err != nil {
		return err
	}
	for _, p := range d.people {
		p.rateEvents = closedEvents
	}
	rows, err := q.QueryContext(ctx, `SELECT a.person_id, sm.status, COUNT(*),
			SUM(a.status IN ('all_shifts', 'window'))
		FROM availability a
		JOIN events e ON e.id = a.event_id
		JOIN schedule_months sm ON sm.month = substr(e.event_date, 1, 7)
		WHERE a.person_id IN (`+candidates+`) AND e.event_date > ? AND e.event_date < ?
		GROUP BY a.person_id, sm.status`, d.event.ID, from, monthEnd)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			pid             entity.ID
			status          shifts.MonthStatus
			answered, avail int
		)
		if err := rows.Scan(&pid, &status, &answered, &avail); err != nil {
			rows.Close()
			return err
		}
		p := d.people[pid]
		if p == nil {
			continue
		}
		p.rateAvailable += avail
		if status == shifts.MonthOpen {
			p.rateEvents += answered
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Ratings in force (the latest entry per person and event) for events
	// this season before this one.
	rows, err = q.QueryContext(ctx, `SELECT r.person_id, r.event_id, r.rating FROM rating_entries r
		JOIN events e ON e.id = r.event_id
		WHERE r.person_id IN (`+candidates+`) AND e.event_date > ? AND e.event_date < ?
		ORDER BY r.rated_at, r.id`, d.event.ID, from, on)
	if err != nil {
		return err
	}
	type key struct{ person, event entity.ID }
	current := map[key]staff.Rating{}
	for rows.Next() {
		var (
			k key
			r staff.Rating
		)
		if err := rows.Scan(&k.person, &k.event, &r); err != nil {
			rows.Close()
			return err
		}
		current[k] = r // later entries replace earlier ones
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for k, r := range current {
		if p := d.people[k.person]; p != nil {
			p.ratings = append(p.ratings, r)
		}
	}

	// Worked: checked in, on a committed roster, or rated for the event.
	rows, err = q.QueryContext(ctx, `WITH season AS (
			SELECT id, event_date FROM events WHERE event_date > ? AND event_date < ?
		), cand AS (`+candidates+`), worked AS (
			SELECT c.person_id, c.event_id FROM checkins c JOIN cand USING (person_id)
			UNION SELECT s.person_id, s.event_id FROM event_staff s JOIN cand USING (person_id)
			UNION SELECT r.person_id, r.event_id FROM rating_entries r JOIN cand USING (person_id)
		)
		SELECT w.person_id, COUNT(*), MAX(season.event_date)
		FROM worked w JOIN season ON season.id = w.event_id
		GROUP BY w.person_id`, from, on, d.event.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			pid  entity.ID
			n    int
			last string
		)
		if err := rows.Scan(&pid, &n, &last); err != nil {
			rows.Close()
			return err
		}
		if p := d.people[pid]; p != nil {
			p.worked = n
			if p.lastWorked, err = entity.ParseDate(last); err != nil {
				rows.Close()
				return err
			}
		}
	}
	rows.Close()
	return rows.Err()
}

// heldBefore counts the season's events before this one: the denominator
// of season load.
func (d *eventData) heldBefore(ctx context.Context, q store.DBTX) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE event_date > ? AND event_date < ?`,
		entity.FormatDate(d.event.Date.AddDate(0, -SeasonMonths, 0)), entity.FormatDate(d.event.Date)).Scan(&n)
	return n, err
}

func (d *eventData) loadCapabilities(ctx context.Context, q store.DBTX) error {
	if len(d.people) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT pce.person_id, pce.capability_id FROM person_capability_effective pce
		JOIN availability a ON a.person_id = pce.person_id AND a.event_id = ?
		WHERE pce.state = 'yes'`, d.event.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, cid entity.ID
		if err := rows.Scan(&pid, &cid); err != nil {
			return err
		}
		if p := d.people[pid]; p != nil {
			p.holds[cid] = true
		}
	}
	return rows.Err()
}

func (d *eventData) loadOverrides(ctx context.Context, q store.DBTX) error {
	rows, err := q.QueryContext(ctx, `SELECT o.person_id, o.created_by, COALESCE(b.name, ''), o.created_at, o.note
		FROM signup_overrides o LEFT JOIN persons b ON b.id = o.created_by
		WHERE o.event_id = ? AND o.removed_at IS NULL`, d.event.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			pid entity.ID
			o   Override
			at  string
		)
		if err := rows.Scan(&pid, &o.ByID, &o.ByName, &at, &o.Note); err != nil {
			return err
		}
		if o.At, err = entity.ParseTime(at); err != nil {
			return err
		}
		// An override for someone who has since withdrawn has no effect.
		if p := d.people[pid]; p != nil {
			p.override = &o
		}
	}
	return rows.Err()
}

// --------------------------------------------------------------- ranking --

// fixed are the scores that do not depend on who else is placed.
type fixed struct {
	scores []RuleScore
	points float64
}

func (d *eventData) rank(w Weights) EventRanking {
	out := EventRanking{
		EventID: d.event.ID, Event: d.event.Name, Date: entity.FormatDate(d.event.Date),
		Weights: w.Clone(), Expectation: d.expectation, RosterSize: len(d.roster),
	}
	for _, dp := range d.depts {
		out.Departments = append(out.Departments, d.rankDept(dp, w))
	}
	return out
}

// rankDept orders one department's candidates. Capability match depends
// on what the people already placed have covered, so the order is built
// greedily: at each step every unplaced person is scored against the
// capability need still open, and the best total is placed next (ties to
// the earlier signup). People pulled above the line by an override are
// placed first, so the capability they bring is already counted.
func (d *eventData) rankDept(dp dept, w Weights) DepartmentRanking {
	r := DepartmentRanking{DepartmentID: dp.id, Department: dp.name, Required: dp.required, Signups: len(dp.members)}

	base := map[entity.ID]fixed{}
	for _, id := range dp.members {
		base[id] = d.fixedScores(d.people[id], w)
	}
	remaining := map[entity.ID]int{}
	for c, n := range dp.need {
		remaining[c] = n
	}

	unplaced := append([]entity.ID(nil), dp.members...)
	var overrides, ranked []Candidate
	place := func(id entity.ID) Candidate {
		p := d.people[id]
		cs, capID := d.capabilityScore(p, dp, remaining, w)
		if capID != nil {
			remaining[*capID]--
		}
		b := base[id]
		scores := append(append([]RuleScore(nil), b.scores...), cs)
		sort.Slice(scores, func(i, j int) bool { return ruleIndex(scores[i].Rule) < ruleIndex(scores[j].Rule) })
		return Candidate{
			PersonID: p.id, Name: p.name, Badge: p.badge, Signup: p.signup,
			Total: round2(b.points + cs.Points), Scores: scores, Override: p.override,
			OnRoster: d.roster[id],
		}
	}

	// Overrides first, best base total first.
	var pulled []entity.ID
	for _, id := range unplaced {
		if d.people[id].override != nil {
			pulled = append(pulled, id)
		}
	}
	sort.SliceStable(pulled, func(i, j int) bool { return base[pulled[i]].points > base[pulled[j]].points })
	for _, id := range pulled {
		overrides = append(overrides, place(id))
	}
	unplaced = without(unplaced, pulled)

	for len(unplaced) > 0 {
		best, bestTotal := -1, -1.0
		for i, id := range unplaced {
			cs, _ := d.capabilityScore(d.people[id], dp, remaining, w)
			total := base[id].points + cs.Points
			if best < 0 || total > bestTotal+1e-9 || (math.Abs(total-bestTotal) <= 1e-9 && earlier(d.people[id], d.people[unplaced[best]])) {
				best, bestTotal = i, total
			}
		}
		ranked = append(ranked, place(unplaced[best]))
		unplaced = append(unplaced[:best], unplaced[best+1:]...)
	}

	// The line: overrides take their slots, the ranking fills the rest.
	slots := len(dp.members)
	if dp.required.Headcount != nil {
		slots = *dp.required.Headcount
	}
	free := max(slots-len(overrides), 0)
	for i := range overrides {
		overrides[i].AboveLine = true
	}
	for i := range ranked {
		ranked[i].AboveLine = i < free
		ranked[i].Displaced = !ranked[i].AboveLine && i < slots && len(overrides) > 0
	}
	r.Candidates = append(overrides, ranked...)
	for i := range r.Candidates {
		r.Candidates[i].Rank = i + 1
		if r.Candidates[i].AboveLine {
			r.AboveLine++
		}
	}
	if dp.required.Headcount != nil {
		r.Shortfall = max(*dp.required.Headcount-r.AboveLine, 0)
	}
	return r
}

func earlier(a, b *person) bool {
	if !a.signup.SignedUpAt.Equal(b.signup.SignedUpAt) {
		return a.signup.SignedUpAt.Before(b.signup.SignedUpAt)
	}
	return a.badge < b.badge
}

func without(ids, remove []entity.ID) []entity.ID {
	drop := map[entity.ID]bool{}
	for _, id := range remove {
		drop[id] = true
	}
	var out []entity.ID
	for _, id := range ids {
		if !drop[id] {
			out = append(out, id)
		}
	}
	return out
}

func ruleIndex(rule string) int {
	for i, r := range Rules {
		if r == rule {
			return i
		}
	}
	return len(Rules)
}

func (d *eventData) fixedScores(p *person, w Weights) fixed {
	var f fixed
	add := func(rule string, score float64, detail string) {
		score = math.Round(clamp(score)*10) / 10
		pts := float64(w[rule]) * score / 100
		f.scores = append(f.scores, RuleScore{Rule: rule, Weight: w[rule], Score: score, Points: round2(pts), Detail: detail})
		f.points += pts
	}

	// Availability rate: 100 at the expectation, 0 at half of it.
	if p.rateEvents == 0 {
		add(RuleAvailabilityRate, 100, "no opened events this season yet")
	} else {
		rate := float64(p.rateAvailable) / float64(p.rateEvents)
		floor := d.expectation / 2
		add(RuleAvailabilityRate, 100*(rate-floor)/(d.expectation-floor),
			fmt.Sprintf("available for %d of %d events this season (%.0f%%); expectation %.0f%%",
				p.rateAvailable, p.rateEvents, 100*rate, 100*d.expectation))
	}

	// Performance: mean of ratings in force this season.
	if len(p.ratings) == 0 {
		add(RulePerformanceRating, UnratedScore, "no ratings this season; scored as acceptable")
	} else {
		sum := 0.0
		counts := map[staff.Rating]int{}
		for _, r := range p.ratings {
			sum += ratingScore[r]
			counts[r]++
		}
		add(RulePerformanceRating, sum/float64(len(p.ratings)), ratingSummary(len(p.ratings), counts))
	}

	// Season load: share of this season's events so far not worked.
	held := d.heldCount
	if held == 0 {
		add(RuleSeasonLoad, 100, "no events held yet this season")
	} else {
		add(RuleSeasonLoad, 100*(1-float64(p.worked)/float64(held)),
			fmt.Sprintf("worked %d of %d events this season", p.worked, held))
	}

	// Recency: days since last worked, full marks at RecencyFullDays.
	if p.lastWorked.IsZero() {
		add(RuleRecency, 100, "has not worked this season")
	} else {
		days := int(d.event.Date.Sub(p.lastWorked).Hours() / 24)
		add(RuleRecency, 100*float64(days-1)/float64(RecencyFullDays-1),
			fmt.Sprintf("last worked %s (%d days before)", entity.FormatDate(p.lastWorked), days))
	}

	// Tenure: half-year bands, 3+ is full marks.
	t := staff.Tenure(p.hire, d.event.Date)
	add(RuleTenure, 100*float64(t)/float64(staff.MaxTenure),
		fmt.Sprintf("tenure %s %s (hired %s)", t, map[bool]string{true: "year", false: "years"}[t == 2], entity.FormatDate(p.hire)))
	return f
}

// capabilityScore scores what a person brings against the need still open.
// For each capability they hold that the event needs, the open share is
// remaining/needed; the score is the best such share. It also returns the
// capability they would fill, which placing them consumes.
func (d *eventData) capabilityScore(p *person, dp dept, remaining map[entity.ID]int, w Weights) (RuleScore, *entity.ID) {
	var (
		best     float64
		bestID   *entity.ID
		bestName string
		held     int
	)
	ids := make([]entity.ID, 0, len(dp.need))
	for c := range dp.need {
		ids = append(ids, c)
	}
	sort.Slice(ids, func(i, j int) bool { return dp.capNames[ids[i]] < dp.capNames[ids[j]] })
	for _, c := range ids {
		if !p.holds[c] || dp.need[c] == 0 {
			continue
		}
		held++
		if remaining[c] <= 0 {
			continue
		}
		share := float64(remaining[c]) / float64(dp.need[c])
		if bestID == nil || share > best {
			id := c
			best, bestID, bestName = share, &id, dp.capNames[c]
		}
	}
	var detail string
	switch {
	case len(dp.need) == 0:
		detail = "the event's posts for this department need no specific capability"
	case bestID != nil:
		detail = fmt.Sprintf("holds %s: %d of %d posts still open", bestName, remaining[*bestID], dp.need[*bestID])
	case held > 0:
		detail = "holds needed capabilities, but their posts are already covered"
	default:
		detail = "holds no capability this event needs"
	}
	score := math.Round(100*best*10) / 10
	return RuleScore{
		Rule: RuleCapabilityMatch, Weight: w[RuleCapabilityMatch], Score: score,
		Points: round2(float64(w[RuleCapabilityMatch]) * score / 100), Detail: detail,
	}, bestID
}

func ratingSummary(n int, counts map[staff.Rating]int) string {
	s := fmt.Sprintf("%d ratings this season:", n)
	for _, r := range []staff.Rating{staff.RatingExceptional, staff.RatingAboveAverage,
		staff.RatingAcceptable, staff.RatingBelowPar, staff.RatingNeedsDiscipline} {
		if counts[r] > 0 {
			s += fmt.Sprintf(" %d %s,", counts[r], strings.ReplaceAll(string(r), "_", " "))
		}
	}
	return s[:len(s)-1]
}

func clamp(v float64) float64 { return math.Max(0, math.Min(100, v)) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// rosterOf returns the people on an event's committed roster.
func rosterOf(ctx context.Context, q store.DBTX, eventID entity.ID) (map[entity.ID]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT person_id FROM event_staff WHERE event_id = ?`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[entity.ID]bool{}
	for rows.Next() {
		var id entity.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
