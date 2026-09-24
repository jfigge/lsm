package shifts

import (
	"fmt"
	"slices"
	"testing"

	"lsm/internal/entity"
)

// world builds matcher input by name.
type world struct {
	dept   entity.ID
	caps   map[string]entity.ID
	posts  []MatchPost
	people []MatchPerson
	ids    map[string]entity.ID
	names  map[entity.ID]string
}

func newWorld() *world {
	return &world{dept: entity.NewID(), caps: map[string]entity.ID{}, ids: map[string]entity.ID{}, names: map[entity.ID]string{}}
}

func (w *world) cap(name string) entity.ID {
	if id, ok := w.caps[name]; ok {
		return id
	}
	id := entity.NewID()
	w.caps[name] = id
	return id
}

func (w *world) post(name string, pattern ShiftPattern, desirability int, caps ...string) *MatchPost {
	id := entity.NewID()
	w.ids[name], w.names[id] = id, name
	p := MatchPost{ID: id, Name: name, Dept: w.dept, Pattern: pattern, Headcount: 1, Desirability: desirability}
	for _, c := range caps {
		p.Caps = append(p.Caps, w.cap(c))
	}
	w.posts = append(w.posts, p)
	return &w.posts[len(w.posts)-1]
}

func (w *world) person(name string, tenure int, gender string, caps map[string]string) *MatchPerson {
	id := entity.NewID()
	w.ids[name], w.names[id] = id, name
	p := MatchPerson{ID: id, Name: name, Badge: fmt.Sprintf("%06d", len(w.people)+1), Dept: w.dept, Tenure: tenure,
		Gender: gender, Caps: map[entity.ID]string{}, Queue: map[entity.ID]int{}}
	for c, s := range caps {
		p.Caps[w.cap(c)] = s
	}
	w.people = append(w.people, p)
	return &w.people[len(w.people)-1]
}

func (w *world) queue(person, post string, depth int) {
	for i := range w.people {
		if w.people[i].Name == person {
			w.people[i].Queue[w.ids[post]] = depth
		}
	}
}

// run returns "post/shift" → person names.
func (w *world) run(pinned ...Placement) (map[string]string, MatchResult) {
	res := Match(MatchInput{Posts: w.posts, People: w.people, Pinned: pinned})
	out := map[string]string{}
	for _, p := range res.Placements {
		out[w.names[p.PostID]+"/"+p.Shift] = w.names[p.PersonID]
	}
	return out, res
}

func TestRestrictedAndMinTenureAreFilters(t *testing.T) {
	w := newWorld()
	w.post("X-ray", TwoShiftDoor, 10, "x-ray")
	two := 4
	w.post("Landing", FullSession, 1, "landing").MinTenure = &two
	w.person("Rita", 6, "female", map[string]string{"x-ray": "restricted", "landing": "yes"})
	w.person("Newt", 0, "male", map[string]string{"landing": "yes"})

	got, res := w.run()
	if got["Landing/first"] != "Rita" || got["X-ray/first"] != "Newt" {
		t.Errorf("placements = %v", got)
	}
	if len(res.Empty) != 0 {
		t.Errorf("empty = %v", res.Empty)
	}

	// Nobody eligible: the post stays visibly empty.
	w2 := newWorld()
	w2.post("X-ray", TwoShiftDoor, 10, "x-ray")
	w2.person("Rita", 6, "female", map[string]string{"x-ray": "restricted"})
	got, res = w2.run()
	if len(got) != 0 || len(res.Empty) != 1 {
		t.Errorf("restricted-only: placements %v, empty %v", got, res.Empty)
	}
}

func TestPriorityLadder(t *testing.T) {
	w := newWorld()
	w.post("South X-ray", TwoShiftDoor, 50, "x-ray")
	w.post("Door", TwoShiftDoor, 80)
	w.post("Door 2", TwoShiftDoor, 80)
	w.person("Opted", 6, "male", map[string]string{"x-ray": "yes"})
	w.person("Named", 0, "male", nil)
	w.person("Plain", 3, "male", nil)
	w.queue("Named", "South X-ray", 1)

	got, res := w.run()
	if got["South X-ray/first"] != "Named" {
		t.Errorf("named primary should win the x-ray: %v", got)
	}
	for _, p := range res.Placements {
		if w.names[p.PersonID] == "Named" && p.Reason != "named primary" {
			t.Errorf("reason = %q", p.Reason)
		}
	}
}

func TestTenureFollowsDesirability(t *testing.T) {
	w := newWorld()
	w.post("Press Row", FullSession, 10)
	w.post("Plaza Door", TwoShiftDoor, 90)
	w.person("Veteran", 6, "male", nil)
	w.person("Rookie", 0, "male", nil)
	got, _ := w.run()
	if got["Press Row/first"] != "Veteran" || got["Plaza Door/first"] != "Rookie" {
		t.Errorf("placements = %v", got)
	}
}

// The scarcest post is filled first, so the only x-ray person is not
// spent on a door.
func TestScarcestFirst(t *testing.T) {
	w := newWorld()
	w.post("Door", TwoShiftDoor, 1) // most desirable, anyone can do it
	w.post("X-ray", TwoShiftDoor, 90, "x-ray")
	w.person("Xena", 6, "female", map[string]string{"x-ray": "yes"})
	w.person("Dan", 0, "male", map[string]string{"x-ray": "restricted"})
	got, _ := w.run()
	if got["X-ray/first"] != "Xena" || got["Door/first"] != "Dan" {
		t.Errorf("placements = %v", got)
	}
}

func TestLeadIsMostTenuredInGroupAndPostIsRefilled(t *testing.T) {
	w := newWorld()
	d1 := w.post("East Door 1", FullSession, 70, "doors")
	d2 := w.post("East Door 2", TwoShiftDoor, 80, "doors")
	lead := w.post("East Lead", TwoShiftDoor, 30, "doors")
	lead.IsLead, lead.LeadOf = true, []entity.ID{d1.ID, d2.ID}
	w.person("Old", 6, "male", map[string]string{"doors": "yes"})
	w.person("Mid", 3, "female", map[string]string{"doors": "yes"})
	w.person("New", 0, "male", map[string]string{"doors": "yes"})

	got, res := w.run()
	if got["East Lead/first"] != "Old" {
		t.Fatalf("lead = %q (%v)", got["East Lead/first"], got)
	}
	if got["East Door 1/first"] == "" || got["East Door 2/first"] == "" || len(res.Empty) != 0 {
		t.Errorf("vacated post not refilled: %v, empty %v", got, res.Empty)
	}
	for _, p := range res.Placements {
		if w.names[p.PostID] == "East Lead" && p.Reason != "most tenured in the group (lead default)" {
			t.Errorf("lead reason = %q", p.Reason)
		}
	}

	// A pinned lead is kept, and nobody is promoted over them.
	got, _ = w.run(Placement{PostID: lead.ID, PersonID: w.ids["New"], Shift: ShiftFirst, Pinned: true})
	if _, ok := got["East Lead/first"]; ok {
		t.Errorf("matcher replaced a pinned lead: %v", got)
	}
	if got["East Door 1/first"] == "New" || got["East Door 2/first"] == "New" {
		t.Errorf("pinned person placed twice: %v", got)
	}
}

func TestRoamTeams(t *testing.T) {
	w := newWorld()
	var leads []entity.ID
	for _, lane := range []string{"East", "West"} {
		door := w.post(lane+" Door 2", TwoShiftDoor, 80, "doors")
		lead := w.post(lane+" Lead", TwoShiftDoor, 30, "doors")
		lead.IsLead, lead.LeadOf = true, []entity.ID{door.ID}
		leads = append(leads, lead.ID)
	}
	for _, name := range []string{"Roam Team 1 A", "Roam Team 1 B", "Roam Team 2 A", "Roam Team 2 B"} {
		w.post(name, SecondShiftOnly, 60, "roam team").Pairing = PairingMixedGender
	}
	// Leads-to-be (most tenured) are both men; the door people are one
	// woman and one person whose gender is not recorded.
	w.person("Al", 6, "male", map[string]string{"roam team": "yes"})
	w.person("Bo", 6, "male", map[string]string{"roam team": "yes"})
	w.person("Cy", 0, "female", map[string]string{"roam team": "yes"})
	w.person("Di", 0, "unknown", map[string]string{"roam team": "yes"})

	got, res := w.run()
	if got["East Lead/first"] == "" || got["West Lead/first"] == "" {
		t.Fatalf("leads = %v", got)
	}
	a1, a2 := got["Roam Team 1 A/second"], got["Roam Team 2 A/second"]
	if !slices.Contains([]string{"Al", "Bo"}, a1) || !slices.Contains([]string{"Al", "Bo"}, a2) {
		t.Errorf("door-set leads should start the roam teams: %v", got)
	}
	b := []string{got["Roam Team 1 B/second"], got["Roam Team 2 B/second"]}
	slices.Sort(b)
	if !slices.Equal(b, []string{"Cy", "Di"}) {
		t.Errorf("partners = %v, want Cy and Di (never two men)", b)
	}
	for _, p := range res.Placements {
		if p.Shift != ShiftSecond {
			continue
		}
		withDi := got["Roam Team 1 B/second"] == "Di" && (w.names[p.PostID] == "Roam Team 1 A" || w.names[p.PostID] == "Roam Team 1 B") ||
			got["Roam Team 2 B/second"] == "Di" && (w.names[p.PostID] == "Roam Team 2 A" || w.names[p.PostID] == "Roam Team 2 B")
		if p.NeedsConfirmation != withDi {
			t.Errorf("%s on %s: needs confirmation = %v", w.names[p.PersonID], w.names[p.PostID], p.NeedsConfirmation)
		}
	}
	_ = leads
}

func TestFullSessionHoldersStay(t *testing.T) {
	w := newWorld()
	w.post("Door 1", FullSession, 70)
	w.post("Door 2", TwoShiftDoor, 80)
	w.post("Plaza Door", SecondShiftOnly, 90)
	w.post("Plaza Door 2", SecondShiftOnly, 90)
	w.person("A", 3, "male", nil)
	w.person("B", 3, "male", nil)
	got, res := w.run()
	if got["Plaza Door/second"] == got["Door 1/first"] || got["Plaza Door 2/second"] == got["Door 1/first"] {
		t.Errorf("full-session holder redeployed: %v", got)
	}
	if len(res.Empty) != 1 {
		t.Errorf("one plaza door should stay empty: %v", res.Empty)
	}
}

// SPEC §2 worked example: someone fifth on every landing except 118 is
// pulled in to a fourth landing when only three queued people show up;
// the depth gap is irrelevant once the people above them are absent.
func TestPremiumQueueWorkedExample(t *testing.T) {
	w := newWorld()
	for _, l := range []string{"112", "118", "124", "130", "136"} {
		w.post(l+" Landing", FullSession, 10, "landing")
	}
	w.person("P112", 6, "male", nil)
	w.person("P118", 6, "female", nil)
	w.person("P124", 6, "male", nil)
	w.person("Fifth", 6, "female", nil)
	w.person("Other", 6, "male", map[string]string{"landing": "yes"})
	w.queue("P112", "112 Landing", 1)
	w.queue("P118", "118 Landing", 1)
	w.queue("P124", "124 Landing", 1)
	for _, l := range []string{"112", "124", "130", "136"} {
		w.queue("Fifth", l+" Landing", 5)
	}
	// Absent queue members (the ones ranked 2–4) are simply not on the roster.

	got, _ := w.run()
	for _, want := range []struct{ post, who string }{
		{"112 Landing/first", "P112"}, {"118 Landing/first", "P118"}, {"124 Landing/first", "P124"},
	} {
		if got[want.post] != want.who {
			t.Errorf("%s = %q, want %s", want.post, got[want.post], want.who)
		}
	}
	fifth := ""
	for k, v := range got {
		if v == "Fifth" {
			fifth = k
		}
	}
	if fifth != "130 Landing/first" && fifth != "136 Landing/first" {
		t.Errorf("fifth-placed person = %q, want a remaining landing she is queued on", fifth)
	}
	// The last landing falls through to open assignment.
	placed := 0
	for k := range got {
		if len(k) > 8 && k[4:11] == "Landing" {
			placed++
		}
	}
	if placed != 5 {
		t.Errorf("landings placed = %d (%v)", placed, got)
	}
}

func TestPinnedPlacementsAreKept(t *testing.T) {
	w := newWorld()
	door := w.post("Door", TwoShiftDoor, 80)
	w.post("Glass", FullSession, 30)
	w.person("Pat", 0, "male", nil)
	w.person("Sam", 6, "female", nil)
	got, _ := w.run(Placement{PostID: door.ID, PersonID: w.ids["Sam"], Shift: ShiftFirst, Pinned: true})
	if got["Glass/first"] != "Pat" || len(got) != 1 {
		t.Errorf("placements around a pin = %v", got)
	}
}
