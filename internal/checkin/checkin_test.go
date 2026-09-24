package checkin

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

type fixture struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	loc      *time.Location
	role     entity.ID
	event    entity.ID
	earlier  entity.ID
	door1    entity.ID
	radio    entity.ID
	stopSign entity.ID
	station  entity.ID
	op       entity.ID // reception operator
	now      time.Time
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
	f := &fixture{t: t, ctx: ctx, db: db, loc: loc, now: time.Date(2026, 10, 3, 22, 42, 0, 0, time.UTC)}

	f.role = entity.NewID()
	f.must(staff.InsertRole(ctx, db, staff.Role{ID: f.role, Name: "Event Security"}))
	hockey := entity.NewID()
	f.must(shifts.InsertEventType(ctx, db, shifts.EventType{ID: hockey, Name: "Hockey"}))
	f.event, f.earlier = f.addEvent(hockey, "2026-10-03"), f.addEvent(hockey, "2026-09-30")

	f.radio, f.stopSign = entity.NewID(), entity.NewID()
	f.must(InsertResource(ctx, db, Resource{ID: f.radio, Name: "Radio", Tracked: true}))
	f.must(InsertResource(ctx, db, Resource{ID: f.stopSign, Name: "Stop Sign"}))
	for _, n := range []string{"1", "2", "3"} {
		f.must(InsertResourceInstance(ctx, db, ResourceInstance{ID: entity.NewID(), ResourceID: f.radio, Number: n}))
	}
	f.door1 = entity.NewID()
	f.must(shifts.InsertPosition(ctx, db, shifts.Position{ID: f.door1, Name: "East Fast Door 1", Area: "East Fast",
		DepartmentID: f.role, ShiftPattern: shifts.FullSession, Headcount: 1}))
	f.must(shifts.AddPositionResource(ctx, db, f.door1, f.radio, 1))

	f.station = entity.NewID()
	f.exec(`INSERT INTO stations (id, name, kind, token_hash, created_at) VALUES (?, 'East entrance', 'kiosk', 'x', 't')`, f.station)
	f.op = f.person("100900", "Reception Operator")
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
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fixture) addEvent(typeID entity.ID, day string) entity.ID {
	id := entity.NewID()
	date, _ := entity.ParseDate(day)
	f.must(shifts.InsertEvent(f.ctx, f.db, shifts.Event{ID: id, EventTypeID: typeID, Name: "Hurricanes " + day,
		Date: date, Start: date.Add(21 * time.Hour), End: date.Add(29 * time.Hour)}))
	return id
}

func (f *fixture) person(badge, name string) entity.ID {
	id := entity.NewID()
	hire, _ := entity.ParseDate("2024-01-01")
	f.must(staff.InsertPerson(f.ctx, f.db, staff.Person{ID: id, Badge: badge, Name: name, RoleID: f.role,
		Tier: staff.TierStaff, HireDate: hire, Gender: staff.GenderUnknown, Active: true}))
	return id
}

func (f *fixture) roster(person entity.ID) {
	f.exec(`INSERT INTO event_staff (event_id, person_id, signed_up_at) VALUES (?, ?, 't')`, f.event, person)
}

func (f *fixture) assign(person entity.ID) {
	f.exec(`INSERT INTO assignments (id, event_id, position_id, person_id, shift, source, created_at)
		VALUES (?, ?, ?, ?, 'first', 'manual', 't')`, entity.NewID(), f.event, f.door1, person)
}

func (f *fixture) scan(badge string, at time.Time) KioskResult {
	f.t.Helper()
	r, err := KioskScan(f.ctx, f.db, f.event, badge, f.station, at, f.loc)
	f.must(err)
	return r
}

func (f *fixture) issue(person entity.ID, req IssueRequest, at time.Time) string {
	f.t.Helper()
	w, err := Issue(f.ctx, f.db, f.event, person, req, f.op, at)
	f.must(err)
	return w
}

func TestKioskDay(t *testing.T) {
	f := newFixture(t)
	ann := f.person("100001", "Ann Lee")
	f.roster(ann)
	f.assign(ann)

	if r := f.scan(" 999999 ", f.now); r.Outcome != OutcomeUnknownBadge || !r.SeeReception || r.Heading != "Badge not recognised" {
		t.Errorf("unknown badge = %+v", r)
	}
	if r := f.scan("100\n001", f.now); r.Outcome != OutcomeUnknownBadge {
		t.Errorf("badge with a control character = %+v", r)
	}

	r := f.scan("100001", f.now)
	if r.Outcome != OutcomeCheckedIn || r.Message != "Resources required — see reception" || r.Time != "6:42 pm" || r.Person.Initials != "AL" {
		t.Fatalf("first scan = %+v", r)
	}
	// A double scan at the door does not check anyone out.
	if r := f.scan("100001", f.now.Add(time.Minute)); r.Outcome != OutcomeAlreadyIn || r.Heading != "Checked in at 6:42 pm" {
		t.Errorf("double scan = %+v", r)
	}

	// Reception issues the radio; the kiosk now sends her to her post.
	f.issue(ann, IssueRequest{ResourceID: f.radio, FirstAvailable: true}, f.now.Add(2*time.Minute))
	if r := f.scan("100001", f.now.Add(3*time.Minute)); r.Message != "Go to your post: East Fast Door 1" || r.SeeReception {
		t.Errorf("after issue = %+v", r)
	}

	// Leaving with the radio: the kiosk sends her to reception instead.
	late := f.now.Add(3 * time.Hour)
	if r := f.scan("100001", late); r.Outcome != OutcomeReturnFirst || r.Message != "Return Radio 1 before you leave" {
		t.Fatalf("leaving with a radio = %+v", r)
	}
	d, err := DeskFor(f.ctx, f.db, f.event, ann, f.loc)
	f.must(err)
	if d.Visit.CheckedOutAt != nil {
		t.Fatal("checked out while holding a radio")
	}
	f.must(Return(f.ctx, f.db, f.event, ann, d.Issued[0].ID, f.op, late))
	if r := f.scan("100001", late.Add(time.Minute)); r.Outcome != OutcomeCheckedOut {
		t.Errorf("check-out scan = %+v", r)
	}
	if r := f.scan("100001", late.Add(2*time.Minute)); r.Outcome != OutcomeAlreadyOut || !r.SeeReception {
		t.Errorf("scan after leaving = %+v", r)
	}
}

func TestKioskNeverBlank(t *testing.T) {
	f := newFixture(t)
	ann := f.person("100001", "Ann Lee")
	f.person("100002", "Ben Ode")

	// No roster committed: nobody can be "not scheduled", only unassigned.
	if r := f.scan("100001", f.now); r.Message != "No assignment — see reception" {
		t.Errorf("no roster = %+v", r)
	}
	// With a roster, someone off it is told so, distinctly.
	f.roster(ann)
	if r := f.scan("100002", f.now); r.Message != "Not on tonight's roster — see reception" || !r.SeeReception {
		t.Errorf("off the roster = %+v", r)
	}
}

func TestIssueNeverBlocks(t *testing.T) {
	f := newFixture(t)
	ann, ben := f.person("100001", "Ann Lee"), f.person("100002", "Ben Ode")

	// Issuing checks in at reception if needed.
	if w := f.issue(ann, IssueRequest{ResourceID: f.radio, FirstAvailable: true}, f.now); w != "" {
		t.Errorf("warning = %q", w)
	}
	d, _ := DeskFor(f.ctx, f.db, f.event, ann, f.loc)
	if d.Visit == nil || d.Visit.InLocation != "reception" {
		t.Fatalf("issue did not check in at reception: %+v", d.Visit)
	}
	avail, _ := Available(f.ctx, f.db, f.radio)
	if !slices.Equal(avail, []string{"2", "3"}) {
		t.Errorf("available after issuing 1 = %v", avail)
	}

	// A number already out is issued anyway, with a warning.
	if w := f.issue(ben, IssueRequest{ResourceID: f.radio, Number: "1"}, f.now); !strings.Contains(w, "still recorded as issued to Ann Lee") {
		t.Errorf("duplicate number warning = %q", w)
	}
	// An unregistered number is recorded as typed.
	if w := f.issue(ben, IssueRequest{ResourceID: f.radio, Number: "99"}, f.now); !strings.Contains(w, "not on the register") {
		t.Errorf("unregistered warning = %q", w)
	}
	d, _ = DeskFor(f.ctx, f.db, f.event, ben, f.loc)
	var typed *Issued
	for i := range d.Issued {
		if d.Issued[i].Number == "99" {
			typed = &d.Issued[i]
		}
	}
	if typed == nil || typed.Registered {
		t.Errorf("free-typed radio = %+v", typed)
	}
	// Untracked resources carry no number.
	f.issue(ben, IssueRequest{ResourceID: f.stopSign, Number: "7"}, f.now)
	var sign int
	f.must(f.db.QueryRow(`SELECT COUNT(*) FROM resource_issues WHERE resource_id = ? AND instance_number IS NULL`, f.stopSign).Scan(&sign))
	if sign != 1 {
		t.Error("stop sign recorded with a number")
	}

	// A tracked resource needs a number or "first available".
	if _, err := Issue(f.ctx, f.db, f.event, ann, IssueRequest{ResourceID: f.radio}, f.op, f.now); !errors.Is(err, ErrInput) {
		t.Errorf("no number: err = %v", err)
	}
	// Someone else's issue cannot be returned through this person.
	if err := Return(f.ctx, f.db, f.event, ann, typed.ID, f.op, f.now); !errors.Is(err, ErrNotFound) {
		t.Errorf("returning another person's radio: err = %v", err)
	}
}

func TestRadiosOutFromEarlierNightsStayOut(t *testing.T) {
	f := newFixture(t)
	ann := f.person("100001", "Ann Lee")
	_, err := Issue(f.ctx, f.db, f.earlier, ann, IssueRequest{ResourceID: f.radio, Number: "2"}, f.op, f.now.AddDate(0, 0, -3))
	f.must(err)
	avail, _ := Available(f.ctx, f.db, f.radio)
	if !slices.Equal(avail, []string{"1", "3"}) {
		t.Errorf("available with radio 2 never returned = %v", avail)
	}
}

func TestCheckInIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ann := f.person("100001", "Ann Lee")
	f.scan("100001", f.now)
	created, err := CheckIn(f.ctx, f.db, f.event, ann, Where{Location: "reception", By: entity.Some(f.op)}, f.now.Add(time.Hour))
	f.must(err)
	d, _ := DeskFor(f.ctx, f.db, f.event, ann, f.loc)
	if created || d.Visit.InLocation != "kiosk" || d.Visit.CheckedIn != "6:42 pm" || d.Visit.InStation != "East entrance" {
		t.Errorf("reception after kiosk = created %v, %+v", created, d.Visit)
	}
	if err := CheckOut(f.ctx, f.db, f.earlier, ann, Where{Location: "reception"}, f.now); !errors.Is(err, ErrNotCheckedIn) {
		t.Errorf("checking out of an event never attended: err = %v", err)
	}
}

func TestSearchPeople(t *testing.T) {
	f := newFixture(t)
	f.person("100001", "Ann Lee")
	f.person("200001", "Anne_Marie Stone")
	for q, want := range map[string]int{"ann": 2, "lee": 1, "1000": 1, "_": 0, "e_m": 1, "a": 0} {
		got, err := SearchPeople(f.ctx, f.db, q, 10)
		f.must(err)
		if len(got) != want {
			t.Errorf("search %q = %d results, want %d", q, len(got), want)
		}
	}
}
