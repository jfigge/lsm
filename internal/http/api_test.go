package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lsm/internal/auth"
	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/staff"
	"lsm/internal/store"
)

func TestMain(m *testing.M) {
	auth.Iterations = 1000
	os.Exit(m.Run())
}

type client struct {
	t     *testing.T
	base  string
	token string
}

// do sends a request and decodes the JSON response into out (if non-nil),
// returning the status and, for errors, the error code.
func (c *client) do(method, path string, body any, out any) (int, string) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return res.StatusCode, e.Error.Code
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
	return res.StatusCode, ""
}

func (c *client) signIn(user, password string) map[string]any {
	c.t.Helper()
	var out map[string]any
	if status, code := c.do("POST", "/api/v1/session", map[string]string{"username": user, "password": password}, &out); status != 200 {
		c.t.Fatalf("sign in %s: %d %s", user, status, code)
	}
	c.token = out["token"].(string)
	return out
}

func apiFixture(t *testing.T) (*sql.DB, string, entity.ID) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	role, hockey, event := entity.NewID(), entity.NewID(), entity.NewID()
	must(staff.InsertRole(ctx, db, staff.Role{ID: role, Name: "Event Security"}))
	hire, _ := entity.ParseDate("2024-08-24")
	for _, p := range []struct {
		badge, name, email, password string
		tier                         staff.Tier
		legacy                       bool
	}{
		{"100000", "Jason Figge", "jason.figge@lenovo.com", "jfigge", staff.TierAdmin, true},
		{"100001", "Sofia Turner", "sofia.turner@lenovo.com", "sofia-password", staff.TierStaff, false},
	} {
		id := entity.NewID()
		must(staff.InsertPerson(ctx, db, staff.Person{ID: id, Badge: p.badge, Name: p.name, RoleID: role, Tier: p.tier,
			HireDate: hire, Gender: staff.GenderFemale, Email: p.email, Active: true}))
		hash, _ := auth.HashPassword(p.password)
		if p.legacy {
			sum := sha256.Sum256([]byte(p.password))
			hash = "sha256:" + hex.EncodeToString(sum[:])
		}
		must(staff.InsertCredential(ctx, db, staff.Credential{PersonID: id, Username: p.email,
			PasswordHash: hash, MustChangePassword: p.legacy}))
	}
	must(shifts.InsertEventType(ctx, db, shifts.EventType{ID: hockey, Name: "Hockey"}))
	date, _ := entity.ParseDate("2026-10-03")
	start := time.Date(2026, 10, 3, 17, 30, 0, 0, time.UTC)
	must(shifts.InsertEvent(ctx, db, shifts.Event{ID: event, EventTypeID: hockey, Name: "Hurricanes vs Ottawa Senators",
		Date: date, Start: start, End: start.Add(8 * time.Hour)}))
	must(shifts.InsertShiftWindow(ctx, db, shifts.ShiftWindow{ID: entity.NewID(), EventID: event, Start: start, End: start.Add(8 * time.Hour)}))
	must(shifts.InsertScheduleMonth(ctx, db, shifts.ScheduleMonth{ID: entity.NewID(), Month: "2026-10", Status: shifts.MonthOpen, ChangedAt: start}))

	loc, _ := time.LoadLocation("America/New_York")
	srv := httptest.NewServer(NewHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Location: loc}))
	t.Cleanup(srv.Close)
	return db, srv.URL, event
}

func TestAPIFlow(t *testing.T) {
	_, base, event := apiFixture(t)
	eventPath := "/api/v1/events/" + event.String()

	anon := &client{t: t, base: base}
	if status, code := anon.do("GET", "/api/v1/months", nil, nil); status != 401 || code != "unauthenticated" {
		t.Errorf("anonymous: %d %s", status, code)
	}
	if status, code := anon.do("POST", "/api/v1/session", map[string]string{"username": "jason.figge@lenovo.com", "password": "x"}, nil); status != 401 || code != "bad_credentials" {
		t.Errorf("wrong password: %d %s", status, code)
	}
	if status, code := anon.do("GET", "/api/v1/nope", nil, nil); status != 404 || code != "not_found" {
		t.Errorf("unknown endpoint: %d %s", status, code)
	}

	// The seeded admin must change the demo password before anything else.
	admin := &client{t: t, base: base}
	if out := admin.signIn("jason.figge@lenovo.com", "jfigge"); out["must_change_password"] != true {
		t.Errorf("sign-in = %v", out)
	}
	if status, code := admin.do("GET", "/api/v1/months", nil, nil); status != 403 || code != "password_change_required" {
		t.Errorf("before password change: %d %s", status, code)
	}
	if status, code := admin.do("POST", "/api/v1/me/password", map[string]string{"current_password": "jfigge", "new_password": "short"}, nil); status != 400 || code != "weak_password" {
		t.Errorf("weak password: %d %s", status, code)
	}
	if status, _ := admin.do("POST", "/api/v1/me/password", map[string]string{"current_password": "jfigge", "new_password": "a-better-password"}, nil); status != 200 {
		t.Fatalf("password change: %d", status)
	}

	// Staff: own profile (no gender), own availability, own ranking only.
	staffer := &client{t: t, base: base}
	staffer.signIn("sofia.turner@lenovo.com", "sofia-password")
	var me map[string]any
	staffer.do("GET", "/api/v1/me", nil, &me)
	if _, leaked := me["gender"]; leaked || me["tier"] != "staff" || me["tenure_years"] != "2" {
		t.Errorf("profile = %v", me)
	}
	var month struct {
		Status string
		Events []struct {
			ID      string
			Windows []struct{ ID, Label string }
		}
	}
	staffer.do("GET", "/api/v1/availability/2026-10", nil, &month)
	if month.Status != "open" || len(month.Events) != 1 || month.Events[0].Windows[0].Label != "1:30–9:30 pm" {
		t.Fatalf("availability = %+v", month)
	}
	var set struct {
		Rate struct{ Available, Events int }
	}
	if status, code := staffer.do("PUT", eventPath+"/availability",
		map[string]any{"status": "window", "window_id": month.Events[0].Windows[0].ID}, &set); status != 200 || set.Rate.Available != 1 {
		t.Errorf("sign up: %d %s %+v", status, code, set)
	}
	if status, code := staffer.do("PUT", eventPath+"/availability", map[string]any{"status": "maybe"}, nil); status != 400 || code != "invalid_request" {
		t.Errorf("invalid status: %d %s", status, code)
	}
	if status, code := staffer.do("GET", eventPath+"/ranking", nil, nil); status != 403 || code != "forbidden" {
		t.Errorf("staff reading the full ranking: %d %s", status, code)
	}
	if status, _ := staffer.do("PUT", "/api/v1/months/2026-11", map[string]string{"status": "open"}, nil); status != 403 {
		t.Errorf("staff opening a month: %d", status)
	}
	var mine struct {
		SignedUp  bool `json:"signed_up"`
		Candidate struct {
			AboveLine bool `json:"above_line"`
			Scores    []struct{ Rule, Detail string }
		}
	}
	staffer.do("GET", eventPath+"/ranking/me", nil, &mine)
	if !mine.SignedUp || !mine.Candidate.AboveLine || len(mine.Candidate.Scores) != 6 {
		t.Errorf("own ranking = %+v", mine)
	}

	// Admin: full ranking, weights, preview, roster, months.
	var ranking struct {
		Ranking struct {
			Departments []struct {
				Signups    int
				Candidates []struct{ Name string }
			}
		}
		Windows []struct{ Label string }
	}
	if status, _ := admin.do("GET", eventPath+"/ranking", nil, &ranking); status != 200 ||
		ranking.Ranking.Departments[0].Signups != 1 || len(ranking.Windows) != 1 {
		t.Errorf("ranking = %d %+v", status, ranking)
	}
	var rules struct {
		Rules []struct {
			Rule   string
			Weight int
		}
		Expectation float64 `json:"availability_expectation_percent"`
	}
	admin.do("PUT", "/api/v1/ranking/rules", map[string]any{"weights": map[string]int{"tenure": 0}, "availability_expectation_percent": 65}, &rules)
	if rules.Expectation != 65 || rules.Rules[5].Rule != "tenure" || rules.Rules[5].Weight != 0 {
		t.Errorf("rules = %+v", rules)
	}
	if status, code := admin.do("PUT", "/api/v1/ranking/rules", map[string]any{"weights": map[string]int{"tenure": -5}}, nil); status != 400 {
		t.Errorf("negative weight: %d %s", status, code)
	}
	var preview struct {
		Events []struct{ Entering, Leaving []any }
	}
	if status, _ := admin.do("POST", "/api/v1/ranking/preview", map[string]any{"month": "2026-10", "weights": map[string]int{"recency": 50}}, &preview); status != 200 || len(preview.Events) != 1 {
		t.Errorf("preview = %d %+v", status, preview)
	}
	var overview []struct {
		Name        string
		Departments []struct {
			Signups   int
			AboveLine int `json:"above_line"`
		}
	}
	if status, _ := admin.do("GET", "/api/v1/months/2026-10/events", nil, &overview); status != 200 ||
		len(overview) != 1 || overview[0].Departments[0].AboveLine != 1 {
		t.Errorf("month overview = %d %+v", status, overview)
	}
	var depts, headcount []map[string]any
	if status, _ := admin.do("GET", "/api/v1/departments", nil, &depts); status != 200 || len(depts) != 1 {
		t.Errorf("departments = %d %v", status, depts)
	}
	if status, _ := admin.do("GET", "/api/v1/headcount", nil, &headcount); status != 200 || len(headcount) != 1 {
		t.Errorf("headcount = %d %v", status, headcount)
	}
	if status, _ := staffer.do("GET", "/api/v1/months/2026-10/events", nil, nil); status != 403 {
		t.Errorf("staff reading the month overview: %d", status)
	}
	var roster struct{ Added int }
	if admin.do("POST", eventPath+"/roster", nil, &roster); roster.Added != 1 {
		t.Errorf("roster = %+v", roster)
	}
	var matched struct {
		Summary struct{ Pool int }
		Board   struct {
			RosterSize int `json:"roster_size"`
		}
	}
	if status, _ := admin.do("POST", eventPath+"/match", nil, &matched); status != 200 || matched.Summary.Pool != 1 || matched.Board.RosterSize != 1 {
		t.Errorf("match = %d %+v", status, matched)
	}
	if status, _ := staffer.do("POST", eventPath+"/match", nil, nil); status != 403 {
		t.Errorf("staff running the matcher: %d", status)
	}
	if status, _ := admin.do("PUT", eventPath+"/published", map[string]bool{"published": true}, nil); status != 200 {
		t.Errorf("publish: %d", status)
	}
	if status, _ := admin.do("PUT", "/api/v1/months/2026-10", map[string]string{"status": "closed"}, nil); status != 200 {
		t.Errorf("closing month: %d", status)
	}
	if status, code := staffer.do("PUT", eventPath+"/availability", map[string]any{"status": "not_available"}, nil); status != 409 || code != "month_not_open" {
		t.Errorf("changing a closed month: %d %s", status, code)
	}

	var home struct {
		Name     string
		Upcoming []any
	}
	if status, _ := staffer.do("GET", "/api/v1/me/home", nil, &home); status != 200 || home.Name != "Sofia Turner" {
		t.Errorf("home = %d %+v", status, home)
	}
	var shifts []struct{ Status, TimeLabel string }
	if status, _ := staffer.do("GET", "/api/v1/me/shifts?month=2026-10", nil, &shifts); status != 200 || len(shifts) != 1 || shifts[0].Status != "rostered" {
		t.Errorf("shifts after the roster commit = %d %+v", status, shifts)
	}
	if status, code := staffer.do("GET", "/api/v1/me/shifts?from=2026-10-01", nil, nil); status != 400 {
		t.Errorf("shifts without a range: %d %s", status, code)
	}

	// Kiosk: an admin pairs a station; its token scans and does nothing else.
	var pair struct{ Token string }
	if status, _ := admin.do("POST", "/api/v1/stations", map[string]string{"name": "East entrance"}, &pair); status != 200 || !strings.HasPrefix(pair.Token, "st_") {
		t.Fatalf("pairing = %d %+v", status, pair)
	}
	kiosk := &client{t: t, base: base, token: pair.Token}
	var scan struct{ Outcome, Heading, Message string }
	if status, _ := kiosk.do("POST", "/api/v1/kiosk/scan", map[string]any{"event_id": event, "badge": "100001"}, &scan); status != 200 ||
		scan.Outcome != "checked_in" || scan.Message != "No assignment — see reception" {
		t.Errorf("kiosk scan = %d %+v", status, scan)
	}
	if status, _ := kiosk.do("GET", "/api/v1/me", nil, nil); status != 403 {
		t.Errorf("station token reading /me: %d", status)
	}
	if status, _ := kiosk.do("GET", eventPath+"/ranking", nil, nil); status != 403 {
		t.Errorf("station token reading a ranking: %d", status)
	}
	if status, _ := staffer.do("POST", "/api/v1/kiosk/scan", map[string]any{"event_id": event, "badge": "100001"}, nil); status != 403 {
		t.Errorf("person token scanning at a kiosk: %d", status)
	}

	// Reception: badge lookup, unknown badge, issue and return.
	var d struct {
		Person struct{ ID, Name string }
		Visit  *struct {
			InLocation string `json:"in_location"`
		}
		Fields []struct{ ID, Name string }
	}
	if status, _ := admin.do("GET", eventPath+"/scan/100001", nil, &d); status != 200 || d.Visit == nil || d.Visit.InLocation != "kiosk" {
		t.Errorf("reception lookup = %d %+v", status, d)
	}
	if status, code := admin.do("GET", eventPath+"/scan/424242", nil, nil); status != 404 || code != "badge_unknown" {
		t.Errorf("unknown badge at reception: %d %s", status, code)
	}
	if status, _ := staffer.do("GET", eventPath+"/scan/100001", nil, nil); status != 403 {
		t.Errorf("staff using reception: %d", status)
	}
	var stations []struct{ Name string }
	if admin.do("GET", "/api/v1/stations", nil, &stations); len(stations) != 1 {
		t.Errorf("stations = %+v", stations)
	}

	if status, _ := staffer.do("DELETE", "/api/v1/session", nil, nil); status != 200 {
		t.Errorf("sign out: %d", status)
	}
	if status, _ := staffer.do("GET", "/api/v1/me", nil, nil); status != 401 {
		t.Errorf("after sign out: %d", status)
	}
}
