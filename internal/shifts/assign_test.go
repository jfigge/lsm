package shifts

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"lsm/internal/entity"
	"lsm/internal/staff"
	"lsm/internal/store"
)

type dbFixture struct {
	t       *testing.T
	ctx     context.Context
	db      *sql.DB
	event   entity.ID
	sec     entity.ID
	landing entity.ID
	door    entity.ID
	plaza   entity.ID
	lead    entity.ID
	people  map[string]entity.ID
	admin   entity.ID
	now     time.Time
}

func newDBFixture(t *testing.T) *dbFixture {
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
	f := &dbFixture{t: t, ctx: ctx, db: db, people: map[string]entity.ID{}, now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	f.sec = entity.NewID()
	f.must(staff.InsertRole(ctx, db, staff.Role{ID: f.sec, Name: "Event Security"}))
	hockey := entity.NewID()
	f.must(InsertEventType(ctx, db, EventType{ID: hockey, Name: "Hockey"}))
	f.event = entity.NewID()
	date, _ := entity.ParseDate("2026-10-03")
	f.must(InsertEvent(ctx, db, Event{ID: f.event, EventTypeID: hockey, Name: "Hurricanes", Date: date,
		Start: date.Add(21 * time.Hour), End: date.Add(29 * time.Hour)}))

	four := 4
	f.landing = f.post(Position{Name: "NE Landing", Area: "Landings", ShiftPattern: FullSession, Desirability: 10, MinTenure: &four}, hockey)
	f.door = f.post(Position{Name: "East Door 2", Area: "East", ShiftPattern: TwoShiftDoor, Desirability: 80}, hockey)
	f.lead = f.post(Position{Name: "East Lead", Area: "East", ShiftPattern: TwoShiftDoor, Desirability: 30, IsLead: true}, hockey)
	f.must(AddLeadPoolMember(ctx, db, f.lead, f.door))
	f.plaza = f.post(Position{Name: "Plaza Door 1", Area: "Plaza", ShiftPattern: SecondShiftOnly, Desirability: 90}, hockey)

	f.admin = f.person("Admin", "2015-01-01", false)
	for name, hire := range map[string]string{"Vet": "2018-01-01", "Mid": "2024-01-01", "New": "2026-06-01"} {
		f.person(name, hire, true)
	}
	return f
}

func (f *dbFixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *dbFixture) post(p Position, eventType entity.ID) entity.ID {
	p.ID, p.DepartmentID, p.Headcount = entity.NewID(), f.sec, 1
	f.must(InsertPosition(f.ctx, f.db, p))
	f.must(AddPositionEventType(f.ctx, f.db, p.ID, eventType))
	return p.ID
}

func (f *dbFixture) person(name, hire string, rostered bool) entity.ID {
	id := entity.NewID()
	h, _ := entity.ParseDate(hire)
	f.must(staff.InsertPerson(f.ctx, f.db, staff.Person{ID: id, Badge: "B-" + name, Name: name, RoleID: f.sec,
		Tier: staff.TierStaff, HireDate: h, Gender: staff.GenderUnknown, Active: true}))
	if rostered {
		_, err := f.db.Exec(`INSERT INTO event_staff (event_id, person_id, signed_up_at) VALUES (?, ?, 't')`, f.event, id)
		f.must(err)
	}
	f.people[name] = id
	return id
}

func (f *dbFixture) board() Board {
	f.t.Helper()
	b, err := AssignmentBoard(f.ctx, f.db, f.event, time.UTC)
	f.must(err)
	return b
}

// post returns the named post's first occupant and its fill notation.
func (b Board) find(name string) (BoardPost, bool) {
	for _, d := range b.Departments {
		for _, p := range d.Posts {
			if p.Name == name {
				return p, true
			}
		}
	}
	return BoardPost{}, false
}

func TestMatcherRunsFromTheRoster(t *testing.T) {
	f := newDBFixture(t)
	sum, err := RunMatcher(f.ctx, f.db, f.event, MatchOptions{}, f.admin, f.now)
	f.must(err)
	if sum.Pool != 3 || sum.Placed != 4 || sum.Empty != 0 {
		t.Errorf("summary = %+v", sum)
	}
	b := f.board()
	landing, _ := b.find("NE Landing")
	lead, _ := b.find("East Lead")
	door, _ := b.find("East Door 2")
	plaza, _ := b.find("Plaza Door 1")
	// Only Vet meets the landing's two years; the lead is the most tenured
	// left in the lane; the door person redeploys to the plaza.
	if landing.Occupants[0].Name != "Vet" || lead.Occupants[0].Name != "Mid" || door.Occupants[0].Name != "New" {
		t.Errorf("landing %v, lead %v, door %v", landing.Occupants, lead.Occupants, door.Occupants)
	}
	if then := plaza.Occupants[0].Then; then != "East Door 2" && then != "East Lead" {
		t.Errorf("plaza should take a freed door person: %+v", plaza.Occupants)
	}
	if landing.Fill != "1 of 1" || b.Departments[0].FirstFilled != 3 || b.RosterSize != 3 {
		t.Errorf("fill = %s, dept = %+v", landing.Fill, b.Departments[0])
	}
}

func TestNoRosterNoMatch(t *testing.T) {
	f := newDBFixture(t)
	_, err := f.db.Exec(`DELETE FROM event_staff`)
	f.must(err)
	if _, err := RunMatcher(f.ctx, f.db, f.event, MatchOptions{}, f.admin, f.now); !errors.Is(err, ErrNoRoster) {
		t.Errorf("err = %v", err)
	}
}

func TestOverridesArePinnedRecordedAndNeverRefused(t *testing.T) {
	f := newDBFixture(t)
	_, err := RunMatcher(f.ctx, f.db, f.event, MatchOptions{}, f.admin, f.now)
	f.must(err)

	// New is below the landing's minimum tenure: allowed, and recorded.
	f.must(Place(f.ctx, f.db, f.event, f.landing, f.people["New"], ShiftFirst, f.admin, "covering for sickness", f.now))
	b := f.board()
	landing, _ := b.find("NE Landing")
	if landing.Fill != "2 of 1" {
		t.Errorf("over-assignment should be legal and shown: %s", landing.Fill)
	}
	var placed *Occupant
	for i := range landing.Occupants {
		if landing.Occupants[i].Name == "New" {
			placed = &landing.Occupants[i]
		}
	}
	if placed == nil || !placed.Pinned || placed.Source != "manual" {
		t.Fatalf("manual placement = %+v", placed)
	}
	kinds := map[string]bool{}
	for _, o := range placed.Overrides {
		kinds[o.Kind] = true
		if o.By != "Admin" || o.Note != "covering for sickness" {
			t.Errorf("override = %+v", o)
		}
	}
	if !kinds["min_tenure"] || !kinds["placement"] {
		t.Errorf("override kinds = %v", kinds)
	}
	// New left their door post: one post per shift.
	if door, _ := b.find("East Door 2"); len(door.Occupants) != 0 {
		t.Errorf("door still holds %v", door.Occupants)
	}

	// A re-run keeps the pinned placement and refills around it.
	_, err = RunMatcher(f.ctx, f.db, f.event, MatchOptions{}, f.admin, f.now)
	f.must(err)
	b = f.board()
	landing, _ = b.find("NE Landing")
	found := false
	for _, o := range landing.Occupants {
		found = found || o.Name == "New"
	}
	if !found {
		t.Error("re-run undid a manual placement")
	}

	// Unassigning and pinning.
	f.must(Unassign(f.ctx, f.db, f.event, placed.AssignmentID, f.admin, f.now))
	if err := Unassign(f.ctx, f.db, f.event, placed.AssignmentID, f.admin, f.now); !errors.Is(err, ErrNotFound) {
		t.Errorf("double unassign: err = %v", err)
	}
	if err := Place(f.ctx, f.db, f.event, f.plaza, f.people["New"], ShiftFirst, f.admin, "", f.now); !errors.Is(err, ErrWrongShift) {
		t.Errorf("plaza in first shift: err = %v", err)
	}

	f.must(Publish(f.ctx, f.db, f.event, true, f.admin, f.now))
	if b := f.board(); b.PublishedAt == "" || b.MatchedAt == "" {
		t.Errorf("published %q matched %q", b.PublishedAt, b.MatchedAt)
	}
}
