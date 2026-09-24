// Package seed loads the demo data in seed/ into the database (SPEC §7).
//
// Two kinds of file live there:
//
//   - Authored reference data: catalog.json (roles, event types,
//     capabilities, resources) and positions.json (one record per post,
//     SPEC §4). These are decoded strictly: an unknown key is an error.
//   - Supplied data, loaded as-is: staff.json, events.json,
//     availability.json and ratings.json. events.json carries fixed event
//     and shift-window UUIDs that the other two reference, so its ids are
//     used verbatim. Availability and ratings name people by badge, which
//     is resolved to the person's id here.
//
// Loading is one transaction and insert-if-absent by natural key: rows
// that already exist are never updated, so re-running the seed after an
// admin has edited the data changes nothing. Every problem found is
// reported together, and any problem rolls the whole load back.
package seed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lsm/internal/checkin"
	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

// Count is what the load did to one table.
type Count struct {
	Table    string
	Inserted int
	Skipped  int // already present
}

// Summary lists per-table counts in load order.
type Summary []Count

// Inserted returns the total rows inserted.
func (s Summary) Inserted() int {
	n := 0
	for _, c := range s {
		n += c.Inserted
	}
	return n
}

// Load reads the seed files in dir and inserts whatever is not already in
// db. Local wall-clock times in the seed (shift windows) are interpreted
// in loc, the venue's time zone.
func Load(ctx context.Context, db *sql.DB, dir string, loc *time.Location) (Summary, error) {
	var f files
	if err := f.read(dir); err != nil {
		return nil, err
	}
	l := &loader{f: f, loc: loc, now: time.Now(), counts: map[string]int{}}
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		l.q = tx
		for _, step := range []func(context.Context) error{
			l.loadRoles, l.loadEventTypes, l.loadCapabilities, l.loadResources, l.loadPositions,
			l.loadPersons, l.loadEvents, l.loadAvailability, l.loadRatings,
		} {
			if err := step(ctx); err != nil {
				return err
			}
		}
		if len(l.problems) > 0 {
			return fmt.Errorf("seed: %d problem(s), nothing loaded:\n  %s",
				len(l.problems), strings.Join(l.problems, "\n  "))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return l.summary, nil
}

// ------------------------------------------------------------- files --

type files struct {
	catalog      catalogFile
	positions    []positionRec
	staff        []staffRec
	events       []eventRec
	availability []availabilityRec
	ratings      []ratingRec
}

type catalogFile struct {
	Roles        []treeRec       `json:"roles"`
	EventTypes   []treeRec       `json:"event_types"`
	Capabilities []capabilityRec `json:"capabilities"`
	Resources    []resourceRec   `json:"resources"`
}

// treeRec is a node of the role or event-type hierarchy. Match lists event
// name prefixes that classify a supplied event as this type (events.json
// has no type field).
type treeRec struct {
	Name     string    `json:"name"`
	Match    []string  `json:"match,omitempty"`
	Children []treeRec `json:"children,omitempty"`
}

type capabilityRec struct {
	Name            string   `json:"name"`
	StaffSelectable bool     `json:"staff_selectable"`
	Roles           []string `json:"roles"`
}

type resourceRec struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Tracked       bool   `json:"tracked"`
	InstanceCount int    `json:"instance_count"`
}

type positionRec struct {
	Name          string   `json:"name"`
	Department    string   `json:"department"`
	Area          string   `json:"area"`
	Floor         string   `json:"floor"`
	Location      string   `json:"location"`
	ShiftPattern  string   `json:"shift_pattern"`
	NeedsBreaking bool     `json:"needs_breaking"`
	IsLead        bool     `json:"is_lead"`
	Desirability  int      `json:"desirability"`
	Headcount     *int     `json:"headcount"` // default 1
	PairingRule   string   `json:"pairing_rule"`
	MinTenure     *int     `json:"min_tenure_half_years"`
	Capabilities  []string `json:"capabilities"`
	// EventTypes omitted means every top-level type (subtypes inherit).
	EventTypes []string `json:"event_types"`
	Resources  []string `json:"resources"`
	LeadOf     []string `json:"lead_of"`
	Notes      string   `json:"notes"`
}

// staffRec is a staff.json record. Fields the system does not keep are
// deliberately absent: tenure_half_years (tenure is computed from
// hire_date), real, and initial_password, which must never be stored.
type staffRec struct {
	Badge              string            `json:"badge"`
	Name               string            `json:"name"`
	Role               string            `json:"role"`
	SubRole            *string           `json:"sub_role"`
	HireDate           string            `json:"hire_date"`
	Capabilities       map[string]string `json:"capabilities"`
	Gender             string            `json:"gender"`
	Tier               string            `json:"tier"`
	Email              string            `json:"email"`
	PasswordHash       string            `json:"password_hash"`
	MustChangePassword bool              `json:"must_change_password"`
}

type eventRec struct {
	ID           string      `json:"id"`
	Date         string      `json:"date"`
	Name         string      `json:"name"`
	Month        string      `json:"month"`
	Status       string      `json:"status"`
	ShiftWindows []windowRec `json:"shift_windows"`
}

type windowRec struct {
	ID    string `json:"id"`
	Start string `json:"start"` // local HH:MM on the event date
	End   string `json:"end"`
}

type availabilityRec struct {
	PersonBadge string  `json:"person_badge"`
	EventID     string  `json:"event_id"`
	WindowID    *string `json:"window_id"`
	Status      string  `json:"status"`
}

type ratingRec struct {
	PersonBadge  string `json:"person_badge"`
	EventID      string `json:"event_id"`
	Rating       string `json:"rating"`
	RatedByBadge string `json:"rated_by_badge"`
	RatedAt      string `json:"rated_at"`
	Source       string `json:"source"`
}

func (f *files) read(dir string) error {
	for _, r := range []struct {
		name   string
		v      any
		strict bool
	}{
		{"catalog.json", &f.catalog, true},
		{"positions.json", &f.positions, true},
		{"staff.json", &f.staff, false},
		{"events.json", &f.events, false},
		{"availability.json", &f.availability, false},
		{"ratings.json", &f.ratings, false},
	} {
		b, err := os.ReadFile(filepath.Join(dir, r.name))
		if err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		if r.strict {
			dec.DisallowUnknownFields()
		}
		if err := dec.Decode(r.v); err != nil {
			return fmt.Errorf("seed: %s: %w", r.name, err)
		}
	}
	return nil
}

// ------------------------------------------------------------ loader --

type loader struct {
	f   files
	q   store.DBTX
	loc *time.Location
	now time.Time

	problems []string
	summary  Summary
	counts   map[string]int // table → index in summary

	roles       map[string]entity.ID // "Concessions/Windy's"
	eventTypes  map[string]entity.ID // by name; names are unique across the tree
	topTypes    []entity.ID
	typeMatch   []typeMatch
	caps        map[string]entity.ID
	resources   map[string]entity.ID
	positions   map[string]entity.ID
	badges      map[string]entity.ID
	events      map[entity.ID]bool
	windowEvent map[entity.ID]entity.ID
}

type typeMatch struct {
	prefix string
	id     entity.ID
}

func (l *loader) problem(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

func (l *loader) count(table string, inserted bool) {
	i, ok := l.counts[table]
	if !ok {
		i = len(l.summary)
		l.counts[table] = i
		l.summary = append(l.summary, Count{Table: table})
	}
	c := &l.summary[i]
	if inserted {
		c.Inserted++
	} else {
		c.Skipped++
	}
}

func (l *loader) loadRoles(ctx context.Context) error {
	existing, err := staff.ListRoles(ctx, l.q)
	if err != nil {
		return err
	}
	l.roles = treePaths(existing, func(r staff.Role) (entity.ID, entity.NullID, string) {
		return r.ID, r.ParentID, r.Name
	})
	var walk func(nodes []treeRec, parent entity.NullID, prefix string) error
	walk = func(nodes []treeRec, parent entity.NullID, prefix string) error {
		for i, n := range nodes {
			path := prefix + n.Name
			id, ok := l.roles[path]
			if !ok {
				id = entity.NewID()
				if err := staff.InsertRole(ctx, l.q, staff.Role{ID: id, ParentID: parent, Name: n.Name, SortOrder: i}); err != nil {
					return err
				}
				l.roles[path] = id
			}
			l.count("roles", !ok)
			if err := walk(n.Children, entity.Some(id), path+"/"); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(l.f.catalog.Roles, entity.NullID{}, "")
}

func (l *loader) loadEventTypes(ctx context.Context) error {
	existing, err := shifts.ListEventTypes(ctx, l.q)
	if err != nil {
		return err
	}
	l.eventTypes = map[string]entity.ID{}
	for _, t := range existing {
		l.eventTypes[t.Name] = t.ID
		if !t.ParentID.Valid {
			l.topTypes = append(l.topTypes, t.ID)
		}
	}
	var walk func(nodes []treeRec, parent entity.NullID) error
	walk = func(nodes []treeRec, parent entity.NullID) error {
		for i, n := range nodes {
			id, ok := l.eventTypes[n.Name]
			if !ok {
				id = entity.NewID()
				if err := shifts.InsertEventType(ctx, l.q, shifts.EventType{ID: id, ParentID: parent, Name: n.Name, SortOrder: i}); err != nil {
					return err
				}
				l.eventTypes[n.Name] = id
				if !parent.Valid {
					l.topTypes = append(l.topTypes, id)
				}
			}
			l.count("event_types", !ok)
			for _, m := range n.Match {
				l.typeMatch = append(l.typeMatch, typeMatch{prefix: m, id: id})
			}
			if err := walk(n.Children, entity.Some(id)); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(l.f.catalog.EventTypes, entity.NullID{})
}

func (l *loader) loadCapabilities(ctx context.Context) error {
	existing, err := staff.ListCapabilities(ctx, l.q)
	if err != nil {
		return err
	}
	l.caps = map[string]entity.ID{}
	for _, c := range existing {
		l.caps[c.Name] = c.ID
	}
	for i, c := range l.f.catalog.Capabilities {
		if _, ok := l.caps[c.Name]; ok {
			l.count("capabilities", false)
			continue
		}
		id := entity.NewID()
		if err := staff.InsertCapability(ctx, l.q, staff.Capability{
			ID: id, Name: c.Name, StaffSelectable: c.StaffSelectable, SortOrder: i,
		}); err != nil {
			return err
		}
		l.caps[c.Name] = id
		l.count("capabilities", true)
		for _, r := range c.Roles {
			roleID, ok := l.roles[r]
			if !ok {
				l.problem("capability %q: unknown role %q", c.Name, r)
				continue
			}
			if err := staff.AddCapabilityRole(ctx, l.q, id, roleID); err != nil {
				return err
			}
			l.count("capability_roles", true)
		}
	}
	return nil
}

func (l *loader) loadResources(ctx context.Context) error {
	existing, err := checkin.ListResources(ctx, l.q)
	if err != nil {
		return err
	}
	l.resources = map[string]entity.ID{}
	for _, r := range existing {
		l.resources[r.Name] = r.ID
	}
	for _, r := range l.f.catalog.Resources {
		if _, ok := l.resources[r.Name]; ok {
			l.count("resources", false)
			continue
		}
		if r.InstanceCount > 0 && !r.Tracked {
			l.problem("resource %q: untracked resources have no numbered instances", r.Name)
		}
		id := entity.NewID()
		if err := checkin.InsertResource(ctx, l.q, checkin.Resource{
			ID: id, Name: r.Name, Description: r.Description, Tracked: r.Tracked,
		}); err != nil {
			return err
		}
		l.resources[r.Name] = id
		l.count("resources", true)
		for n := 1; n <= r.InstanceCount; n++ {
			if err := checkin.InsertResourceInstance(ctx, l.q, checkin.ResourceInstance{
				ID: entity.NewID(), ResourceID: id, Number: fmt.Sprint(n),
			}); err != nil {
				return err
			}
			l.count("resource_instances", true)
		}
	}
	return nil
}

func (l *loader) loadPositions(ctx context.Context) error {
	existing, err := shifts.ListPositions(ctx, l.q)
	if err != nil {
		return err
	}
	l.positions = map[string]entity.ID{}
	for _, p := range existing {
		l.positions[p.Name] = p.ID
	}
	// Links are added for new positions only, so an admin's later edits to
	// an existing post's mappings are never re-added by a re-seed. Lead
	// pools are resolved after every position exists.
	var leads []positionRec
	inFile := map[string]bool{}
	for _, r := range l.f.positions {
		if inFile[r.Name] {
			l.problem("position %q appears twice", r.Name)
			continue
		}
		inFile[r.Name] = true
		if _, ok := l.positions[r.Name]; ok {
			l.count("positions", false)
			continue
		}
		dept, ok := l.roles[r.Department]
		if !ok {
			l.problem("position %q: unknown department %q", r.Name, r.Department)
			continue
		}
		p := shifts.Position{
			ID: entity.NewID(), Name: r.Name, DepartmentID: dept, Area: r.Area, Location: r.Location,
			Floor: r.Floor, ShiftPattern: shifts.ShiftPattern(r.ShiftPattern), NeedsBreaking: r.NeedsBreaking,
			IsLead: r.IsLead, Desirability: r.Desirability, Headcount: 1, PairingRule: r.PairingRule,
			MinTenure: r.MinTenure, Notes: r.Notes,
		}
		if r.Headcount != nil {
			p.Headcount = *r.Headcount
		}
		if !p.ShiftPattern.Valid() {
			l.problem("position %q: unknown shift pattern %q", r.Name, r.ShiftPattern)
			continue
		}
		if err := shifts.InsertPosition(ctx, l.q, p); err != nil {
			return err
		}
		l.positions[r.Name] = p.ID
		l.count("positions", true)

		for _, c := range r.Capabilities {
			if id, ok := l.caps[c]; !ok {
				l.problem("position %q: unknown capability %q", r.Name, c)
			} else if err := shifts.AddPositionCapability(ctx, l.q, p.ID, id); err != nil {
				return err
			}
		}
		types := l.topTypes
		if r.EventTypes != nil {
			types = nil
			for _, t := range r.EventTypes {
				if id, ok := l.eventTypes[t]; ok {
					types = append(types, id)
				} else {
					l.problem("position %q: unknown event type %q", r.Name, t)
				}
			}
		}
		for _, id := range types {
			if err := shifts.AddPositionEventType(ctx, l.q, p.ID, id); err != nil {
				return err
			}
		}
		for _, res := range r.Resources {
			if id, ok := l.resources[res]; !ok {
				l.problem("position %q: unknown resource %q", r.Name, res)
			} else if err := shifts.AddPositionResource(ctx, l.q, p.ID, id, 1); err != nil {
				return err
			}
		}
		if len(r.LeadOf) > 0 {
			leads = append(leads, r)
		}
	}
	for _, r := range leads {
		for _, member := range r.LeadOf {
			id, ok := l.positions[member]
			if !ok {
				l.problem("position %q: lead of unknown position %q", r.Name, member)
				continue
			}
			if err := shifts.AddLeadPoolMember(ctx, l.q, l.positions[r.Name], id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *loader) loadPersons(ctx context.Context) error {
	existing, err := staff.ListPersons(ctx, l.q)
	if err != nil {
		return err
	}
	l.badges = map[string]entity.ID{}
	for _, p := range existing {
		l.badges[p.Badge] = p.ID
	}
	inFile := map[string]bool{}
	for _, r := range l.f.staff {
		if r.Badge == "" || inFile[r.Badge] {
			l.problem("staff %q: missing or duplicate badge %q", r.Name, r.Badge)
			continue
		}
		inFile[r.Badge] = true
		if _, ok := l.badges[r.Badge]; ok {
			l.count("persons", false)
			continue
		}
		rolePath := r.Role
		if r.SubRole != nil && *r.SubRole != "" {
			rolePath += "/" + *r.SubRole
		}
		roleID, ok := l.roles[rolePath]
		if !ok {
			l.problem("staff %s: unknown role %q", r.Badge, rolePath)
			continue
		}
		hire, err := entity.ParseDate(r.HireDate)
		if err != nil {
			l.problem("staff %s: %v", r.Badge, err)
			continue
		}
		gender := staff.Gender(r.Gender)
		if gender == "" {
			gender = staff.GenderUnknown
		}
		tier := staff.Tier(r.Tier)
		if tier == "" {
			tier = staff.TierStaff
		}
		if !gender.Valid() || !tier.Valid() {
			l.problem("staff %s: invalid gender %q or tier %q", r.Badge, r.Gender, r.Tier)
			continue
		}
		p := staff.Person{
			ID: entity.NewID(), Badge: r.Badge, Name: r.Name, RoleID: roleID, Tier: tier,
			HireDate: hire, Gender: gender, Email: strings.ToLower(r.Email), Active: true,
			CreatedAt: l.now, UpdatedAt: l.now,
		}
		if err := staff.InsertPerson(ctx, l.q, p); err != nil {
			return err
		}
		l.badges[r.Badge] = p.ID
		l.count("persons", true)

		if p.Email != "" {
			scheme, _, ok := strings.Cut(r.PasswordHash, ":")
			if !ok || scheme == "" {
				l.problem("staff %s: password_hash must be \"<scheme>:<hash>\"", r.Badge)
			} else if err := staff.InsertCredential(ctx, l.q, staff.Credential{
				PersonID: p.ID, Username: p.Email, PasswordHash: r.PasswordHash,
				MustChangePassword: r.MustChangePassword, UpdatedAt: l.now,
			}); err != nil {
				return err
			} else {
				l.count("credentials", true)
			}
		}

		for name, v := range r.Capabilities {
			capID, ok := l.caps[name]
			if !ok {
				l.problem("staff %s: unknown capability %q", r.Badge, name)
				continue
			}
			state := staff.CapabilityState(strings.ReplaceAll(v, " ", "_"))
			if !state.Valid() {
				l.problem("staff %s: capability %q: invalid state %q", r.Badge, name, v)
				continue
			}
			// No row already means no preference.
			if state == staff.StateNoPreference {
				continue
			}
			if err := staff.SetPersonCapability(ctx, l.q, p.ID, capID, state, entity.NullID{}, l.now); err != nil {
				return err
			}
			l.count("person_capabilities", true)
		}
	}
	return nil
}

func (l *loader) loadEvents(ctx context.Context) error {
	existing, err := shifts.ListEvents(ctx, l.q)
	if err != nil {
		return err
	}
	l.events = map[entity.ID]bool{}
	for _, e := range existing {
		l.events[e.ID] = true
	}
	l.windowEvent = map[entity.ID]entity.ID{}
	rows, err := l.q.QueryContext(ctx, `SELECT id, event_id FROM shift_windows`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var w, e entity.ID
		if err := rows.Scan(&w, &e); err != nil {
			rows.Close()
			return err
		}
		l.windowEvent[w] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	monthStatus := map[string]string{}
	var months []string
	for _, r := range l.f.events {
		id, err := entity.ParseID(r.ID)
		if err != nil {
			l.problem("event %q: %v", r.Name, err)
			continue
		}
		date, err := entity.ParseDate(r.Date)
		if err != nil {
			l.problem("event %s: %v", r.ID, err)
			continue
		}
		month := r.Date[:7]
		if r.Month != "" && r.Month != month {
			l.problem("event %s: month %q does not match date %s", r.ID, r.Month, r.Date)
		}
		if prev, ok := monthStatus[month]; !ok {
			monthStatus[month] = r.Status
			months = append(months, month)
		} else if prev != r.Status {
			l.problem("month %s has events with both %q and %q status", month, prev, r.Status)
		}

		if l.events[id] {
			l.count("events", false)
			for _, w := range r.ShiftWindows {
				if wid, err := entity.ParseID(w.ID); err == nil {
					l.windowEvent[wid] = id
				}
			}
			continue
		}
		typeID, ok := l.classify(r.Name)
		if !ok {
			l.problem("event %s %q: no event type matches its name", r.ID, r.Name)
			continue
		}
		if len(r.ShiftWindows) == 0 {
			l.problem("event %s: no shift windows", r.ID)
			continue
		}
		var windows []shifts.ShiftWindow
		for i, w := range r.ShiftWindows {
			wid, err := entity.ParseID(w.ID)
			if err != nil {
				l.problem("event %s window: %v", r.ID, err)
				continue
			}
			start, err1 := l.localTime(date, w.Start)
			end, err2 := l.localTime(date, w.End)
			if err := errors.Join(err1, err2); err != nil {
				l.problem("event %s window %s: %v", r.ID, w.ID, err)
				continue
			}
			windows = append(windows, shifts.ShiftWindow{ID: wid, EventID: id, Start: start, End: end, SortOrder: i})
		}
		if len(windows) == 0 {
			continue
		}
		e := shifts.Event{ID: id, EventTypeID: typeID, Name: r.Name, Date: date,
			Start: windows[0].Start, End: windows[0].End}
		for _, w := range windows {
			if w.Start.Before(e.Start) {
				e.Start = w.Start
			}
			if w.End.After(e.End) {
				e.End = w.End
			}
		}
		if err := shifts.InsertEvent(ctx, l.q, e); err != nil {
			return err
		}
		l.events[id] = true
		l.count("events", true)
		for _, w := range windows {
			if err := shifts.InsertShiftWindow(ctx, l.q, w); err != nil {
				return err
			}
			l.windowEvent[w.ID] = id
			l.count("shift_windows", true)
		}
	}

	opened, err := shifts.ListScheduleMonths(ctx, l.q)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, m := range opened {
		have[m.Month] = true
	}
	for _, month := range months {
		if have[month] {
			l.count("schedule_months", false)
			continue
		}
		status := shifts.MonthStatus(monthStatus[month])
		if status != shifts.MonthOpen && status != shifts.MonthClosed {
			l.problem("month %s: invalid status %q", month, status)
			continue
		}
		if err := shifts.InsertScheduleMonth(ctx, l.q, shifts.ScheduleMonth{
			ID: entity.NewID(), Month: month, Status: status, ChangedAt: l.now,
		}); err != nil {
			return err
		}
		l.count("schedule_months", true)
	}
	return nil
}

// classify picks the event type whose match prefix is the longest one the
// event name starts with.
func (l *loader) classify(name string) (entity.ID, bool) {
	var best typeMatch
	for _, m := range l.typeMatch {
		if strings.HasPrefix(name, m.prefix) && len(m.prefix) > len(best.prefix) {
			best = m
		}
	}
	return best.id, best.prefix != ""
}

func (l *loader) localTime(date time.Time, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q", hhmm)
	}
	y, m, d := date.Date()
	return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, l.loc), nil
}

func (l *loader) loadAvailability(ctx context.Context) error {
	type key struct{ person, event entity.ID }
	have := map[key]bool{}
	rows, err := l.q.QueryContext(ctx, `SELECT person_id, event_id FROM availability`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.person, &k.event); err != nil {
			rows.Close()
			return err
		}
		have[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for i, r := range l.f.availability {
		person, ok := l.badges[r.PersonBadge]
		if !ok {
			l.problem("availability row %d: unknown badge %q", i, r.PersonBadge)
			continue
		}
		event, err := entity.ParseID(r.EventID)
		if err != nil || !l.events[event] {
			l.problem("availability row %d: unknown event %q", i, r.EventID)
			continue
		}
		k := key{person, event}
		if have[k] {
			l.count("availability", false)
			continue
		}
		a := shifts.Availability{
			ID: entity.NewID(), PersonID: person, EventID: event,
			Status: shifts.AvailabilityStatus(strings.ReplaceAll(r.Status, " ", "_")),
			// The seed has no signup times; the tiebreak only means
			// something for signups made through the app.
			SignedUpAt: l.now,
		}
		if !a.Status.Valid() {
			l.problem("availability row %d: invalid status %q", i, r.Status)
			continue
		}
		if r.WindowID != nil {
			w, err := entity.ParseID(*r.WindowID)
			if err != nil || l.windowEvent[w] != event {
				l.problem("availability row %d: window %q is not a window of event %s", i, *r.WindowID, r.EventID)
				continue
			}
			a.WindowID = entity.Some(w)
		}
		if (a.Status == shifts.OneWindow) != a.WindowID.Valid {
			l.problem("availability row %d: status %q does not agree with window_id", i, r.Status)
			continue
		}
		if err := shifts.InsertAvailability(ctx, l.q, a); err != nil {
			return err
		}
		have[k] = true
		l.count("availability", true)
	}
	return nil
}

func (l *loader) loadRatings(ctx context.Context) error {
	type key struct {
		person, event, by entity.ID
		at                string
	}
	have := map[key]bool{}
	rows, err := l.q.QueryContext(ctx, `SELECT person_id, event_id, rated_by, rated_at FROM rating_entries`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			k  key
			by entity.NullID
		)
		if err := rows.Scan(&k.person, &k.event, &by, &k.at); err != nil {
			rows.Close()
			return err
		}
		k.by = by.ID
		have[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for i, r := range l.f.ratings {
		person, ok1 := l.badges[r.PersonBadge]
		rater, ok2 := l.badges[r.RatedByBadge]
		if !ok1 || !ok2 {
			l.problem("ratings row %d: unknown badge %q or rater %q", i, r.PersonBadge, r.RatedByBadge)
			continue
		}
		event, err := entity.ParseID(r.EventID)
		if err != nil || !l.events[event] {
			l.problem("ratings row %d: unknown event %q", i, r.EventID)
			continue
		}
		at, err := entity.ParseTime(r.RatedAt)
		if err != nil {
			l.problem("ratings row %d: %v", i, err)
			continue
		}
		e := staff.RatingEntry{
			ID: entity.NewID(), PersonID: person, EventID: event,
			Rating:  staff.Rating(strings.ReplaceAll(r.Rating, " ", "_")),
			RatedBy: entity.Some(rater), RatedAt: at, Source: staff.RatingSource(r.Source),
		}
		if !e.Rating.Valid() {
			l.problem("ratings row %d: invalid rating %q", i, r.Rating)
			continue
		}
		if e.Source == "" {
			e.Source = staff.SourceSeed
		}
		k := key{person, event, rater, entity.FormatTime(at)}
		if have[k] {
			l.count("rating_entries", false)
			continue
		}
		if err := staff.AppendRating(ctx, l.q, e); err != nil {
			return err
		}
		have[k] = true
		l.count("rating_entries", true)
	}
	return nil
}

// treePaths maps "Parent/Child" paths to ids for a self-referencing table.
func treePaths[T any](nodes []T, get func(T) (entity.ID, entity.NullID, string)) map[string]entity.ID {
	type node struct {
		parent entity.NullID
		name   string
	}
	byID := map[entity.ID]node{}
	for _, n := range nodes {
		id, parent, name := get(n)
		byID[id] = node{parent, name}
	}
	out := map[string]entity.ID{}
	for id := range byID {
		path, cur, depth := "", id, 0
		for {
			n := byID[cur]
			if path == "" {
				path = n.name
			} else {
				path = n.name + "/" + path
			}
			if !n.parent.Valid || depth > len(byID) {
				break
			}
			cur, depth = n.parent.ID, depth+1
		}
		out[path] = id
	}
	return out
}
