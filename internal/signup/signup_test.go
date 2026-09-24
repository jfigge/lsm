package signup

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
)

var t0 = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func TestAvailability(t *testing.T) {
	f := newFixture(t)
	ann := f.person("Ann", f.security, "2023-01-01")

	v, err := PersonMonth(f.ctx, f.db, ann, "2026-10", f.loc)
	f.must(err)
	if len(v.Events) != 2 || !v.Editable || v.Rate.Events != 2 || v.Rate.Available != 0 || v.Expectation != 60 {
		t.Fatalf("month view = %+v", v)
	}
	if v.Events[0].Windows[0].Label != "1:30–9:30 pm" {
		t.Errorf("window label = %q", v.Events[0].Windows[0].Label)
	}
	if _, err := PersonMonth(f.ctx, f.db, ann, "2026-09", f.loc); !errors.Is(err, ErrNotFound) {
		t.Errorf("unopened month: err = %v, want ErrNotFound", err)
	}

	signedAt := func() time.Time {
		var s string
		f.must(f.db.QueryRow(`SELECT signed_up_at FROM availability WHERE person_id = ? AND event_id = ?`,
			ann, f.target).Scan(&s))
		v, _ := entity.ParseTime(s)
		return v
	}
	set := func(sel Selection, at time.Time) (Rate, error) {
		_, rate, err := SetAvailability(f.ctx, f.db, ann, f.target, sel, at, f.loc)
		return rate, err
	}

	rate, err := set(Selection{Status: shifts.OneWindow, WindowID: &f.windows[1]}, t0)
	f.must(err)
	if rate.Available != 1 || rate.Events != 2 || rate.Percent != 50 {
		t.Errorf("rate = %+v", rate)
	}
	// Moving between windows keeps the original signup time …
	_, err = set(Selection{Status: shifts.AllShifts}, t0.Add(time.Hour))
	f.must(err)
	if !signedAt().Equal(t0) {
		t.Errorf("signup time moved on a window change: %v", signedAt())
	}
	// … withdrawing and re-offering does not.
	_, err = set(Selection{Status: shifts.NotAvailable}, t0.Add(2*time.Hour))
	f.must(err)
	_, err = set(Selection{Status: shifts.AllShifts}, t0.Add(3*time.Hour))
	f.must(err)
	if !signedAt().Equal(t0.Add(3 * time.Hour)) {
		t.Errorf("signup time after re-offering = %v", signedAt())
	}

	var input *InputError
	if _, err := set(Selection{Status: shifts.OneWindow, WindowID: &f.otherWin}, t0); !errors.As(err, &input) {
		t.Errorf("window of another event: err = %v", err)
	}
	if _, err := set(Selection{Status: shifts.OneWindow}, t0); !errors.As(err, &input) {
		t.Errorf("window status without a window: err = %v", err)
	}
	if _, _, err := SetAvailability(f.ctx, f.db, ann, f.june[0], Selection{Status: shifts.AllShifts}, t0, f.loc); !errors.Is(err, ErrMonthNotOpen) {
		t.Errorf("closed month: err = %v", err)
	}
}

func TestRuleScores(t *testing.T) {
	f := newFixture(t)
	ann := f.person("Ann", f.security, "2023-01-01")
	ben := f.person("Ben", f.security, "2026-04-10")
	for _, e := range f.june {
		f.offer(ann, e, shifts.AllShifts, t0)
	}
	f.offer(ben, f.june[0], shifts.AllShifts, t0) // June's other three unanswered: unavailable
	f.rate(ann, f.june[0], staff.RatingExceptional)
	f.rate(ann, f.june[1], staff.RatingAcceptable)
	f.offer(ann, f.target, shifts.AllShifts, t0)
	f.offer(ben, f.target, shifts.AllShifts, t0.Add(time.Minute))

	w, err := LoadWeights(f.ctx, f.db)
	f.must(err)
	d := deptOf(f.rank(w), f.security)
	a, b := find(d, "Ann"), find(d, "Ben")
	for _, c := range []struct {
		who   string
		got   RuleScore
		score float64
	}{
		// Ann: 5 of 5 (four closed June events, and October's answered one).
		{"Ann", score(a, RuleAvailabilityRate), 100},
		{"Ann", score(a, RulePerformanceRating), 75},
		{"Ann", score(a, RuleSeasonLoad), 50}, // worked 2 of 4 held
		{"Ann", score(a, RuleRecency), 100},
		{"Ann", score(a, RuleTenure), 100},
		// Ben: 2 of 5 = 40%, a third of the way from 30% (floor) to 60%.
		{"Ben", score(b, RuleAvailabilityRate), 33.3},
		{"Ben", score(b, RulePerformanceRating), UnratedScore},
		{"Ben", score(b, RuleSeasonLoad), 100},
		{"Ben", score(b, RuleRecency), 100},
		{"Ben", score(b, RuleTenure), 16.7}, // 0.5 years
	} {
		if c.got.Score != c.score {
			t.Errorf("%s %s = %v (%s), want %v", c.who, c.got.Rule, c.got.Score, c.got.Detail, c.score)
		}
	}
	// Points are weight × score / 100 and sum to the total.
	sum := 0.0
	for _, s := range a.Scores {
		sum += s.Points
	}
	if a.Total != round2(sum) || score(a, RuleAvailabilityRate).Points != 25 {
		t.Errorf("Ann total %v, points sum %v", a.Total, sum)
	}
	if d.Required.Source != "posts" || *d.Required.Headcount != 3 {
		t.Errorf("requirement = %+v (second-shift posts must not count)", d.Required)
	}
}

// Capability match is scored against the need still open, so a third
// door person loses out to someone who covers the only x-ray post.
func TestCapabilityMatchIsGreedy(t *testing.T) {
	f := newFixture(t)
	for i, name := range []string{"D1", "D2", "D3"} {
		f.offer(f.person(name, f.security, "2024-01-01", f.doors), f.target, shifts.AllShifts, t0.Add(time.Duration(i)*time.Minute))
	}
	f.offer(f.person("X", f.security, "2024-01-01", f.xray), f.target, shifts.AllShifts, t0.Add(time.Hour))

	d := deptOf(f.rank(only(map[string]int{RuleCapabilityMatch: 100})), f.security)
	var order []string
	for _, c := range d.Candidates {
		order = append(order, c.Name)
	}
	if !slices.Equal(order, []string{"D1", "X", "D2", "D3"}) {
		t.Errorf("order = %v", order)
	}
	if got := above(d); !slices.Equal(got, []string{"D1", "X", "D2"}) {
		t.Errorf("above line = %v", got)
	}
	if s := score(find(d, "D3"), RuleCapabilityMatch); s.Score != 33.3 || s.Detail != "holds doors: 1 of 3 posts still open" {
		t.Errorf("D3 capability = %+v", s)
	}
}

func TestOverridePullsAboveTheLine(t *testing.T) {
	f := newFixture(t)
	admin := f.person("Admin", f.security, "2020-01-01")
	for i, name := range []string{"D1", "D2", "D3"} {
		f.offer(f.person(name, f.security, "2024-01-01", f.doors), f.target, shifts.AllShifts, t0.Add(time.Duration(i)*time.Minute))
	}
	f.offer(f.person("X", f.security, "2024-01-01", f.xray), f.target, shifts.AllShifts, t0.Add(time.Hour))
	w := only(map[string]int{RuleCapabilityMatch: 100})

	f.must(PullAboveLine(f.ctx, f.db, f.target, f.people["D3"], admin, "south door regular", t0))
	d := deptOf(f.rank(w), f.security)
	if got := above(d); !slices.Equal(got, []string{"D3", "X", "D1"}) {
		t.Fatalf("above line with override = %v", got)
	}
	d3, d2 := find(d, "D3"), find(d, "D2")
	if d3.Override == nil || d3.Override.ByName != "Admin" || d3.Override.Note != "south door regular" {
		t.Errorf("override not recorded: %+v", d3.Override)
	}
	if !d2.Displaced || d2.AboveLine {
		t.Errorf("D2 should be displaced below the line: %+v", d2)
	}

	if err := PullAboveLine(f.ctx, f.db, f.target, admin, admin, "", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("override for someone not signed up: err = %v", err)
	}
	// The month overview counts the line without ranking; it must agree.
	overview, err := MonthOverview(f.ctx, f.db, "2026-10")
	f.must(err)
	if got := overview[0].Departments[0]; got.AboveLine != 3 || got.Overrides != 1 || got.BelowLine != 1 {
		t.Errorf("overview = %+v", got)
	}

	f.must(WithdrawOverride(f.ctx, f.db, f.target, f.people["D3"], admin, t0))
	if got := above(deptOf(f.rank(w), f.security)); !slices.Equal(got, []string{"D1", "X", "D2"}) {
		t.Errorf("above line after withdrawal = %v", got)
	}
	var history int
	f.must(f.db.QueryRow(`SELECT COUNT(*) FROM signup_overrides WHERE removed_at IS NOT NULL`).Scan(&history))
	if history != 1 {
		t.Errorf("withdrawn override not kept in history")
	}
}

func TestHeadcountAndDepartments(t *testing.T) {
	f := newFixture(t)
	admin := f.person("Admin", f.security, "2020-01-01")
	f.offer(f.person("S", f.security, "2024-01-01"), f.target, shifts.AllShifts, t0)
	f.offer(f.person("W", f.outlet, "2024-01-01"), f.target, shifts.AllShifts, t0)

	r := f.rank(only(nil))
	if len(r.Departments) != 2 {
		t.Fatalf("departments = %d, want Security and Concessions", len(r.Departments))
	}
	con := r.Departments[0] // top-level roles in name order here
	if con.Department != "Concessions" || con.Required.Headcount != nil || con.AboveLine != 1 {
		t.Errorf("an outlet competes within Concessions, uncapped: %+v", con)
	}
	if sec := deptOf(r, f.security); sec.Shortfall != 2 {
		t.Errorf("security shortfall = %d, want 2 (3 needed, 1 signed up)", sec.Shortfall)
	}

	req := func() Requirement { return deptOf(f.rank(only(nil)), f.security).Required }
	five, one := 5, 1
	f.must(SetHeadcount(f.ctx, f.db, f.security, entity.Some(f.hockey), entity.NullID{}, &five, admin, t0))
	if got := req(); *got.Headcount != 5 || got.Source != "event_type:Hockey" {
		t.Errorf("type override: %+v", got)
	}
	f.must(SetHeadcount(f.ctx, f.db, f.security, entity.NullID{}, entity.Some(f.target), &one, admin, t0))
	if got := req(); *got.Headcount != 1 || got.Source != "event" {
		t.Errorf("event override: %+v", got)
	}
	f.must(SetHeadcount(f.ctx, f.db, f.security, entity.NullID{}, entity.Some(f.target), nil, admin, t0))
	if got := req(); *got.Headcount != 5 {
		t.Errorf("cleared event override: %+v", got)
	}
	var input *InputError
	if err := SetHeadcount(f.ctx, f.db, f.outlet, entity.Some(f.hockey), entity.NullID{}, &five, admin, t0); !errors.As(err, &input) {
		t.Errorf("headcount on a sub-role: err = %v", err)
	}
}

func TestPreviewShowsBothDirections(t *testing.T) {
	f := newFixture(t)
	admin := f.person("Admin", f.security, "2020-01-01")
	one := 1
	f.must(SetHeadcount(f.ctx, f.db, f.security, entity.NullID{}, entity.Some(f.target), &one, admin, t0))
	f.offer(f.person("Old", f.security, "2020-01-01"), f.target, shifts.AllShifts, t0)
	f.offer(f.person("New", f.security, "2026-09-01", f.doors), f.target, shifts.AllShifts, t0)

	byCapability := only(map[string]int{RuleCapabilityMatch: 100})
	byTenure := only(map[string]int{RuleTenure: 100})
	p, err := Preview(f.ctx, f.db, []entity.ID{f.target}, byCapability, Proposal{Weights: byTenure})
	f.must(err)
	if len(p) != 1 || len(p[0].Entering) != 1 || len(p[0].Leaving) != 1 ||
		p[0].Entering[0].Name != "Old" || p[0].Leaving[0].Name != "New" {
		t.Fatalf("preview = %+v", p)
	}
	if e := p[0].Entering[0]; e.RankBefore != 2 || e.RankAfter != 1 {
		t.Errorf("crossing ranks = %+v", e)
	}
	var input *InputError
	if _, err := Preview(f.ctx, f.db, nil, byCapability, Proposal{Weights: Weights{RuleTenure: -1}}); !errors.As(err, &input) {
		t.Errorf("invalid weights: err = %v", err)
	}
}

func TestPersonalRankingAndRoster(t *testing.T) {
	f := newFixture(t)
	for i, name := range []string{"D1", "D2", "D3", "D4"} {
		f.offer(f.person(name, f.security, "2024-01-01", f.doors), f.target, shifts.AllShifts, t0.Add(time.Duration(i)*time.Minute))
	}
	f.must(SaveWeightsFor(t, f, only(map[string]int{RuleCapabilityMatch: 100})))

	me, err := RankFor(f.ctx, f.db, f.target, f.people["D4"])
	f.must(err)
	if !me.SignedUp || me.Candidate.AboveLine || me.Candidate.Rank != 4 || me.LineTotal == nil || !me.Provisional {
		t.Errorf("D4's own view = %+v", me)
	}

	res, err := CommitRoster(f.ctx, f.db, f.target)
	f.must(err)
	if res.Added != 3 || res.Removed != 0 {
		t.Errorf("first commit = %+v", res)
	}
	res, err = CommitRoster(f.ctx, f.db, f.target)
	f.must(err)
	if res.Added != 0 || res.Kept != 3 {
		t.Errorf("re-commit = %+v", res)
	}
	one := 1
	f.must(SetHeadcount(f.ctx, f.db, f.security, entity.NullID{}, entity.Some(f.target), &one, f.people["D1"], t0))
	res, err = CommitRoster(f.ctx, f.db, f.target)
	f.must(err)
	if res.Removed != 2 || res.Kept != 1 {
		t.Errorf("commit after lowering headcount = %+v", res)
	}
	if me, _ := RankFor(f.ctx, f.db, f.target, f.people["D1"]); me.Provisional {
		t.Error("committed roster still reported as provisional")
	}
}

func SaveWeightsFor(t *testing.T, f *fixture, w Weights) error {
	t.Helper()
	_, err := SaveWeights(f.ctx, f.db, w, f.people["D1"], t0)
	return err
}

func TestWeightsAndExpectation(t *testing.T) {
	f := newFixture(t)
	w, err := LoadWeights(f.ctx, f.db)
	f.must(err)
	want := Weights{RuleAvailabilityRate: 25, RulePerformanceRating: 25, RuleCapabilityMatch: 20,
		RuleSeasonLoad: 15, RuleRecency: 10, RuleTenure: 5}
	for k, v := range want {
		if w[k] != v {
			t.Errorf("starting weight %s = %d, want %d", k, w[k], v)
		}
	}
	admin := f.person("Admin", f.security, "2020-01-01")
	got, err := SaveWeights(f.ctx, f.db, Weights{RuleTenure: 0}, admin, t0)
	f.must(err)
	if got[RuleTenure] != 0 || got[RuleRecency] != 10 {
		t.Errorf("partial update = %v", got)
	}
	var by entity.NullID
	f.must(f.db.QueryRow(`SELECT updated_by FROM ranking_rules WHERE rule = ?`, RuleTenure).Scan(&by))
	if by.ID != admin {
		t.Error("weight change not attributed")
	}
	var input *InputError
	if _, err := SaveWeights(f.ctx, f.db, Weights{"vibes": 10}, admin, t0); !errors.As(err, &input) {
		t.Errorf("unknown rule: err = %v", err)
	}

	f.must(SetExpectation(f.ctx, f.db, 0.7, t0))
	if e, _ := Expectation(f.ctx, f.db); e != 0.7 {
		t.Errorf("expectation = %v", e)
	}
	if err := SetExpectation(f.ctx, f.db, 1.5, t0); !errors.As(err, &input) {
		t.Errorf("expectation 150%%: err = %v", err)
	}
}

type recorder struct{ opened []string }

func (r *recorder) MonthOpened(_ context.Context, month string, recipients []string) error {
	r.opened = append(r.opened, month)
	return nil
}

func TestOpeningAMonthNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	admin := f.person("Admin", f.security, "2020-01-01")
	n := &recorder{}
	f.must(SetMonthStatus(f.ctx, f.db, "2026-11", shifts.MonthOpen, admin, t0, n))
	f.must(SetMonthStatus(f.ctx, f.db, "2026-11", shifts.MonthOpen, admin, t0, n))
	f.must(SetMonthStatus(f.ctx, f.db, "2026-11", shifts.MonthClosed, admin, t0, n))
	if !slices.Equal(n.opened, []string{"2026-11"}) {
		t.Errorf("notifications = %v", n.opened)
	}
	months, err := ListMonths(f.ctx, f.db, false)
	f.must(err)
	if len(months) != 3 || months[2].Month != "2026-11" || months[2].Status != shifts.MonthClosed {
		t.Errorf("months = %+v", months)
	}
}
