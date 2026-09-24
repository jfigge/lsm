package signup

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

// fixture is a small arena: Security needs two door posts and an x-ray
// post at hockey (plus a second-shift post that does not count towards
// the requirement), June is closed history and October is open.
type fixture struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	loc      *time.Location
	security entity.ID
	services entity.ID
	outlet   entity.ID // Concessions → Windy's
	hockey   entity.ID
	doors    entity.ID
	xray     entity.ID
	june     []entity.ID // four closed events
	target   entity.ID   // 2026-10-10, open
	windows  []entity.ID // target's windows
	other    entity.ID   // 2026-10-17, open
	otherWin entity.ID
	people   map[string]entity.ID
	badge    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("America/New_York")
	f := &fixture{t: t, ctx: ctx, db: db, loc: loc, people: map[string]entity.ID{}, badge: 200000}

	f.security, f.services = f.role("Event Security", entity.NullID{}), f.role("Event Services", entity.NullID{})
	concessions := f.role("Concessions", entity.NullID{})
	f.outlet = f.role("Windy's", entity.Some(concessions))

	f.hockey = entity.NewID()
	f.must(shifts.InsertEventType(ctx, db, shifts.EventType{ID: f.hockey, Name: "Hockey"}))
	f.doors, f.xray = entity.NewID(), entity.NewID()
	f.must(staff.InsertCapability(ctx, db, staff.Capability{ID: f.doors, Name: "doors", StaffSelectable: true}))
	f.must(staff.InsertCapability(ctx, db, staff.Capability{ID: f.xray, Name: "x-ray", StaffSelectable: true}))
	f.post("Door 1", shifts.FullSession, f.doors)
	f.post("Door 2", shifts.TwoShiftDoor, f.doors)
	f.post("X-ray", shifts.TwoShiftDoor, f.xray)
	f.post("Plaza Door", shifts.SecondShiftOnly, f.doors)

	f.month("2026-06", shifts.MonthClosed)
	f.month("2026-10", shifts.MonthOpen)
	for _, day := range []string{"2026-06-02", "2026-06-09", "2026-06-16", "2026-06-23"} {
		id, _ := f.event(day)
		f.june = append(f.june, id)
	}
	f.target, f.windows = f.event("2026-10-10")
	var w []entity.ID
	f.other, w = f.event("2026-10-17")
	f.otherWin = w[0]
	return f
}

func (f *fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fixture) role(name string, parent entity.NullID) entity.ID {
	id := entity.NewID()
	f.must(staff.InsertRole(f.ctx, f.db, staff.Role{ID: id, ParentID: parent, Name: name}))
	return id
}

func (f *fixture) post(name string, pattern shifts.ShiftPattern, capability entity.ID) {
	id := entity.NewID()
	f.must(shifts.InsertPosition(f.ctx, f.db, shifts.Position{
		ID: id, Name: name, DepartmentID: f.security, ShiftPattern: pattern, Headcount: 1,
	}))
	f.must(shifts.AddPositionCapability(f.ctx, f.db, id, capability))
	f.must(shifts.AddPositionEventType(f.ctx, f.db, id, f.hockey))
}

func (f *fixture) month(m string, s shifts.MonthStatus) {
	f.must(shifts.InsertScheduleMonth(f.ctx, f.db, shifts.ScheduleMonth{
		ID: entity.NewID(), Month: m, Status: s, ChangedAt: time.Now()}))
}

func (f *fixture) event(day string) (entity.ID, []entity.ID) {
	date, _ := entity.ParseDate(day)
	y, m, d := date.Date()
	id := entity.NewID()
	f.must(shifts.InsertEvent(f.ctx, f.db, shifts.Event{
		ID: id, EventTypeID: f.hockey, Name: "Hurricanes vs " + day, Date: date,
		Start: time.Date(y, m, d, 13, 30, 0, 0, f.loc), End: time.Date(y, m, d, 21, 30, 0, 0, f.loc),
	}))
	var windows []entity.ID
	for i, h := range []int{13, 15} {
		w := entity.NewID()
		f.must(shifts.InsertShiftWindow(f.ctx, f.db, shifts.ShiftWindow{
			ID: w, EventID: id, SortOrder: i,
			Start: time.Date(y, m, d, h, 30, 0, 0, f.loc), End: time.Date(y, m, d, 21, 30, 0, 0, f.loc),
		}))
		windows = append(windows, w)
	}
	return id, windows
}

// person adds a member of staff hired on hire, holding the capabilities.
func (f *fixture) person(name string, role entity.ID, hire string, holds ...entity.ID) entity.ID {
	f.badge++
	id := entity.NewID()
	h, _ := entity.ParseDate(hire)
	f.must(staff.InsertPerson(f.ctx, f.db, staff.Person{
		ID: id, Badge: strconv.Itoa(f.badge), Name: name, RoleID: role,
		Tier: staff.TierStaff, HireDate: h, Gender: staff.GenderUnknown, Active: true,
	}))
	for _, c := range holds {
		f.must(staff.SetPersonCapability(f.ctx, f.db, id, c, staff.StateYes, entity.NullID{}, time.Now()))
	}
	f.people[name] = id
	return id
}

// offer records a signup directly, at a fixed signup time.
func (f *fixture) offer(person, event entity.ID, status shifts.AvailabilityStatus, at time.Time) {
	f.must(shifts.InsertAvailability(f.ctx, f.db, shifts.Availability{
		ID: entity.NewID(), PersonID: person, EventID: event, Status: status, SignedUpAt: at}))
}

// rate records a rating (and so a worked event) for person at event.
func (f *fixture) rate(person, event entity.ID, r staff.Rating) {
	f.must(staff.AppendRating(f.ctx, f.db, staff.RatingEntry{
		ID: entity.NewID(), PersonID: person, EventID: event, Rating: r,
		RatedAt: time.Date(2026, 6, 30, 22, 0, 0, 0, time.UTC), Source: staff.SourceSeed}))
}

func (f *fixture) rank(w Weights) EventRanking {
	f.t.Helper()
	r, err := RankEvent(f.ctx, f.db, f.target, w)
	f.must(err)
	return r
}

// only returns weights with every rule off except the named ones.
func only(rules map[string]int) Weights {
	w := Weights{}
	for _, r := range Rules {
		w[r] = rules[r]
	}
	return w
}

func deptOf(r EventRanking, id entity.ID) DepartmentRanking {
	for _, d := range r.Departments {
		if d.DepartmentID == id {
			return d
		}
	}
	return DepartmentRanking{}
}

func above(d DepartmentRanking) []string {
	var out []string
	for _, c := range d.Candidates {
		if c.AboveLine {
			out = append(out, c.Name)
		}
	}
	return out
}

func score(c Candidate, rule string) RuleScore {
	for _, s := range c.Scores {
		if s.Rule == rule {
			return s
		}
	}
	return RuleScore{}
}

func find(d DepartmentRanking, name string) Candidate {
	for _, c := range d.Candidates {
		if c.Name == name {
			return c
		}
	}
	return Candidate{}
}
