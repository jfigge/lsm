package shifts

import (
	"fmt"
	"sort"
	"strings"

	"lsm/internal/entity"
)

// The matcher fills an event's posts from its capped roster (SPEC §2, §5).
// This file is the pure algorithm: no database, so every rule can be
// tested directly. assign.go loads its input and stores its output.
//
// Shape of the night. Everyone on the roster gets one first-shift post.
// Holders of two_shift_door and first_shift_only posts are freed at second
// shift and fill the second_shift_only posts (door-set leads first, onto
// the roam teams); full_session holders stay where they are. Anyone freed
// with no second-shift post is left for break allocation (step 9).
//
// Rules, in the order they apply:
//  1. Pinned placements (manual overrides) are kept and never moved.
//  2. Premium posts with queues are resolved from the queues first.
//  3. Remaining posts are filled scarcest first. Candidates are ranked by
//     the priority ladder — named on the post's queue, then opted in,
//     then no preference — and within a rung, desirable posts go to the
//     most tenured and the rest to the newest.
//  4. Lead posts go to the most tenured person placed in their group,
//     whose own post is then refilled.
//  5. Roam teams take one door-set lead each and a partner of the other
//     gender; an unknown gender fits either half and is flagged for a
//     human to confirm. Gender is used nowhere else.
//
// Restricted people are never placed on a post needing that capability,
// min_tenure is a hard filter, and a post nobody can fill stays empty.

// Shift names as stored on assignments.
const (
	ShiftFirst   = "first"
	ShiftSecond  = "second"
	ShiftBreaker = "breaker"
)

// MatchPerson is a rostered person as the matcher sees them.
type MatchPerson struct {
	ID     entity.ID
	Name   string
	Badge  string
	Dept   entity.ID
	Tenure int    // half-years, capped at 6
	Gender string // used for roam-team pairing only
	// Caps holds effective capability states: "yes" or "restricted"; a
	// capability not present is no preference.
	Caps map[entity.ID]string
	// Queue maps a position to this person's depth on its queue.
	Queue map[entity.ID]int
}

// MatchPost is a position that applies to the event.
type MatchPost struct {
	ID           entity.ID
	Name         string
	Area         string
	Dept         entity.ID
	Pattern      ShiftPattern
	Headcount    int
	Caps         []entity.ID
	MinTenure    *int
	Desirability int
	IsLead       bool
	LeadOf       []entity.ID
	Pairing      string
}

// Placement is one person on one post for one shift.
type Placement struct {
	PostID   entity.ID
	PersonID entity.ID
	Shift    string
	// NeedsConfirmation flags a roam pair a human should check.
	NeedsConfirmation bool
	// Reason explains the choice on the review screen.
	Reason string
	Pinned bool
}

// MatchInput is one event's posts, people and pinned placements.
type MatchInput struct {
	Posts  []MatchPost
	People []MatchPerson
	Pinned []Placement
}

// Slot is one unit of headcount on a post for a shift.
type Slot struct {
	PostID entity.ID
	Shift  string
}

// MatchResult is the matcher's draft.
type MatchResult struct {
	Placements []Placement // new placements, excluding the pinned ones
	Empty      []Slot      // headcount nobody could fill
}

type matcher struct {
	posts  map[entity.ID]*MatchPost
	people map[entity.ID]*MatchPerson
	order  []entity.ID // people in badge order, for determinism
	// placed[shift][person] is the post a person holds in a shift.
	placed map[string]map[entity.ID]entity.ID
	// fill[shift][post] counts placements.
	fill   map[string]map[entity.ID]int
	out    []Placement
	pinned map[string]map[entity.ID]bool // shift → person → pinned
	desir  map[entity.ID]float64         // post → desirability percentile, 0 = most desirable
}

// Match drafts assignments for one event.
func Match(in MatchInput) MatchResult {
	m := &matcher{
		posts:  map[entity.ID]*MatchPost{},
		people: map[entity.ID]*MatchPerson{},
		placed: map[string]map[entity.ID]entity.ID{ShiftFirst: {}, ShiftSecond: {}},
		fill:   map[string]map[entity.ID]int{ShiftFirst: {}, ShiftSecond: {}},
		pinned: map[string]map[entity.ID]bool{ShiftFirst: {}, ShiftSecond: {}},
		desir:  map[entity.ID]float64{},
	}
	for i := range in.Posts {
		m.posts[in.Posts[i].ID] = &in.Posts[i]
	}
	for i := range in.People {
		p := &in.People[i]
		m.people[p.ID] = p
		m.order = append(m.order, p.ID)
	}
	sort.Slice(m.order, func(i, j int) bool { return m.people[m.order[i]].Badge < m.people[m.order[j]].Badge })
	m.percentiles(in.Posts)

	for _, p := range in.Pinned {
		if p.Shift != ShiftFirst && p.Shift != ShiftSecond {
			continue
		}
		m.placed[p.Shift][p.PersonID] = p.PostID
		m.fill[p.Shift][p.PostID]++
		m.pinned[p.Shift][p.PersonID] = true
	}

	// First shift.
	first := m.postsFor(ShiftFirst)
	m.resolveQueues(first)
	m.fillGreedy(ShiftFirst, first)
	m.promoteLeads(first)

	// Second shift: the people freed from first-shift-only and door posts.
	// Paired posts are filled only by fillRoamTeams, which enforces the
	// pairing rule.
	m.fillRoamTeams()
	m.fillGreedy(ShiftSecond, filter(m.postsFor(ShiftSecond), func(p *MatchPost) bool { return p.Pairing == "" }))

	var res MatchResult
	res.Placements = m.out
	for _, shift := range []string{ShiftFirst, ShiftSecond} {
		for _, p := range m.postsFor(shift) {
			for i := m.fill[shift][p.ID]; i < p.Headcount; i++ {
				res.Empty = append(res.Empty, Slot{PostID: p.ID, Shift: shift})
			}
		}
	}
	return res
}

// percentiles ranks each department's posts by desirability, so "the
// desirable half" means the same thing whatever numbers an admin uses.
func (m *matcher) percentiles(posts []MatchPost) {
	byDept := map[entity.ID][]*MatchPost{}
	for i := range posts {
		byDept[posts[i].Dept] = append(byDept[posts[i].Dept], &posts[i])
	}
	for _, ps := range byDept {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Desirability < ps[j].Desirability })
		for i, p := range ps {
			if len(ps) > 1 {
				m.desir[p.ID] = float64(i) / float64(len(ps)-1)
			}
		}
	}
}

// postsFor returns the posts staffed in a shift, most desirable first. A
// full_session post is staffed by its first-shift holder throughout.
func (m *matcher) postsFor(shift string) []*MatchPost {
	var out []*MatchPost
	for _, p := range m.posts {
		switch {
		case shift == ShiftFirst && p.Pattern != SecondShiftOnly,
			shift == ShiftSecond && p.Pattern == SecondShiftOnly:
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Desirability != out[j].Desirability {
			return out[i].Desirability < out[j].Desirability
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func filter(ps []*MatchPost, keep func(*MatchPost) bool) []*MatchPost {
	var out []*MatchPost
	for _, p := range ps {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

// available reports whether a person is free for a shift. In second
// shift only people whose first-shift post releases them are free.
func (m *matcher) available(shift string, person entity.ID) bool {
	if _, busy := m.placed[shift][person]; busy {
		return false
	}
	if shift == ShiftSecond {
		post, ok := m.placed[ShiftFirst][person]
		if !ok {
			return false // not working tonight
		}
		if p := m.posts[post]; p == nil || p.Pattern == FullSession {
			return false
		}
	}
	return true
}

// eligible reports whether a person may be auto-placed on a post: same
// department, not restricted on anything the post needs, and at or above
// its minimum tenure.
func (m *matcher) eligible(p *MatchPerson, post *MatchPost) bool {
	if p.Dept != post.Dept {
		return false
	}
	if post.MinTenure != nil && p.Tenure < *post.MinTenure {
		return false
	}
	for _, c := range post.Caps {
		if p.Caps[c] == "restricted" {
			return false
		}
	}
	return true
}

// rung is the priority ladder: 1..n = queue depth, then opted in, then
// no preference. Lower is better.
func (m *matcher) rung(p *MatchPerson, post *MatchPost) (int, string) {
	if d, ok := p.Queue[post.ID]; ok {
		if d == 1 {
			return 1, "named primary"
		}
		return d, fmt.Sprintf("backup #%d on the post's queue", d-1)
	}
	const opted, noPref = 1000, 2000
	if len(post.Caps) == 0 {
		return noPref, "no capability needed"
	}
	for _, c := range post.Caps {
		if p.Caps[c] == "yes" {
			return opted, "opted in"
		}
	}
	return noPref, "no preference"
}

// better reports whether a is a better candidate than b for post.
func (m *matcher) better(a, b *MatchPerson, post *MatchPost) bool {
	ra, _ := m.rung(a, post)
	rb, _ := m.rung(b, post)
	if ra != rb {
		return ra < rb
	}
	if a.Tenure != b.Tenure {
		// Experienced staff to desirable posts, newer staff to the rest.
		if m.desir[post.ID] < 0.5 {
			return a.Tenure > b.Tenure
		}
		return a.Tenure < b.Tenure
	}
	return a.Badge < b.Badge
}

func (m *matcher) place(shift string, post *MatchPost, person *MatchPerson, reason string, confirm bool) {
	m.placed[shift][person.ID] = post.ID
	m.fill[shift][post.ID]++
	m.out = append(m.out, Placement{PostID: post.ID, PersonID: person.ID, Shift: shift, Reason: reason, NeedsConfirmation: confirm})
}

func (m *matcher) unplace(shift string, person entity.ID) {
	post := m.placed[shift][person]
	delete(m.placed[shift], person)
	m.fill[shift][post]--
	for i, p := range m.out {
		if p.Shift == shift && p.PersonID == person {
			m.out = append(m.out[:i], m.out[i+1:]...)
			return
		}
	}
}

func (m *matcher) open(shift string, post *MatchPost) bool {
	return m.fill[shift][post.ID] < post.Headcount
}

// resolveQueues fills premium posts from their queues (SPEC §2
// "Resolving premium posts on the night"). For each capability group with
// queues: work down every position's queue in depth order among the
// people present; anyone queued in the group whose own positions are all
// taken goes to any open position in the group; the rest falls through
// to open assignment.
func (m *matcher) resolveQueues(first []*MatchPost) {
	groups := map[entity.ID][]*MatchPost{}
	var capOrder []entity.ID
	for _, post := range first {
		queued := false
		for _, id := range m.order {
			if _, ok := m.people[id].Queue[post.ID]; ok {
				queued = true
				break
			}
		}
		if !queued || len(post.Caps) == 0 || post.IsLead {
			continue
		}
		c := post.Caps[0]
		if _, seen := groups[c]; !seen {
			capOrder = append(capOrder, c)
		}
		groups[c] = append(groups[c], post)
	}
	sort.Slice(capOrder, func(i, j int) bool { return capOrder[i].String() < capOrder[j].String() })

	for _, c := range capOrder {
		group := groups[c]
		// Everyone the group's queues name, present and free.
		var pool []*MatchPerson
		maxDepth := 0
		for _, id := range m.order {
			p := m.people[id]
			for _, post := range group {
				if d, ok := p.Queue[post.ID]; ok {
					if d > maxDepth {
						maxDepth = d
					}
					if m.available(ShiftFirst, id) {
						pool = append(pool, p)
						break
					}
				}
			}
		}
		for d := 1; d <= maxDepth; d++ {
			for _, post := range group {
				for _, p := range pool {
					if m.open(ShiftFirst, post) && p.Queue[post.ID] == d && m.available(ShiftFirst, p.ID) && m.eligible(p, post) {
						_, why := m.rung(p, post)
						m.place(ShiftFirst, post, p, why, false)
					}
				}
			}
		}
		// Ranked but outplaced: any open post in the group, best depth first.
		sort.SliceStable(pool, func(i, j int) bool { return bestDepth(pool[i], group) < bestDepth(pool[j], group) })
		for _, p := range pool {
			if !m.available(ShiftFirst, p.ID) {
				continue
			}
			for _, post := range group {
				if m.open(ShiftFirst, post) && m.eligible(p, post) {
					m.place(ShiftFirst, post, p, "queued in this group; their ranked posts were taken", false)
					break
				}
			}
		}
	}
}

func bestDepth(p *MatchPerson, group []*MatchPost) int {
	best := 1 << 30
	for _, post := range group {
		if d, ok := p.Queue[post.ID]; ok && d < best {
			best = d
		}
	}
	return best
}

// fillGreedy fills open headcount scarcest capability first (SPEC §5).
// Scarcity is supply against demand: the free people opted in (or queued)
// for a post's capability, divided by the open headcount needing that
// capability. Posts needing no capability come last. At each step the
// scarcest open post takes its best candidate; a post nobody is eligible
// for is left empty. When there are fewer people than posts, the posts
// left empty are therefore the ones whose skills are most plentiful.
func (m *matcher) fillGreedy(shift string, posts []*MatchPost) {
	key := func(p *MatchPost) string {
		if len(p.Caps) == 0 {
			return ""
		}
		return p.Caps[0].String()
	}
	for {
		demand := map[string]int{}
		for _, post := range posts {
			if m.open(shift, post) {
				demand[key(post)] += post.Headcount - m.fill[shift][post.ID]
			}
		}
		var (
			pick         *MatchPost
			pickRatio    float64
			pickEligible int
		)
		for _, post := range posts {
			if !m.open(shift, post) {
				continue
			}
			eligible, opted := 0, 0
			for _, id := range m.order {
				p := m.people[id]
				if m.available(shift, id) && m.eligible(p, post) {
					eligible++
					if r, _ := m.rung(p, post); r < 2000 && len(post.Caps) > 0 {
						opted++
					}
				}
			}
			if eligible == 0 {
				continue
			}
			ratio := float64(opted) / float64(demand[key(post)])
			if len(post.Caps) == 0 {
				ratio = 1e9 // plentiful by definition
			}
			// posts is desirability-ordered, so remaining ties go to the
			// more desirable post.
			if pick == nil || ratio < pickRatio || (ratio == pickRatio && eligible < pickEligible) {
				pick, pickRatio, pickEligible = post, ratio, eligible
			}
		}
		if pick == nil {
			return
		}
		var best *MatchPerson
		for _, id := range m.order {
			p := m.people[id]
			if m.available(shift, id) && m.eligible(p, pick) && (best == nil || m.better(p, best, pick)) {
				best = p
			}
		}
		_, why := m.rung(best, pick)
		m.place(shift, pick, best, why, false)
	}
}

// promoteLeads makes each lead post's holder the most tenured person in
// its group. Everyone is already placed, so this is a swap: the more
// tenured group member takes the lead post and the previous holder takes
// their post. Pinned people are never moved.
func (m *matcher) promoteLeads(first []*MatchPost) {
	holderOf := func(post entity.ID) []entity.ID {
		var out []entity.ID
		for _, id := range m.order {
			if p, ok := m.placed[ShiftFirst][id]; ok && p == post {
				out = append(out, id)
			}
		}
		return out
	}
	for _, lead := range first {
		if !lead.IsLead || len(lead.LeadOf) == 0 {
			continue
		}
		pool := map[entity.ID]bool{}
		for _, id := range lead.LeadOf {
			pool[id] = true
		}
		for _, holder := range holderOf(lead.ID) {
			if m.pinned[ShiftFirst][holder] {
				continue
			}
			h := m.people[holder]
			var best *MatchPerson
			for _, id := range m.order {
				post, ok := m.placed[ShiftFirst][id]
				p := m.people[id]
				if !ok || !pool[post] || m.pinned[ShiftFirst][id] || !m.eligible(p, lead) || !m.eligible(h, m.posts[post]) {
					continue
				}
				if p.Tenure > h.Tenure && (best == nil || p.Tenure > best.Tenure || (p.Tenure == best.Tenure && p.Badge < best.Badge)) {
					best = p
				}
			}
			reason := "most tenured in the group (lead default)"
			if best != nil {
				member := m.posts[m.placed[ShiftFirst][best.ID]]
				m.unplace(ShiftFirst, best.ID)
				m.unplace(ShiftFirst, holder)
				m.place(ShiftFirst, member, h, "moved off the lead post for a more tenured group member", false)
				m.place(ShiftFirst, lead, best, reason, false)
				continue
			}
			for i := range m.out {
				if m.out[i].Shift == ShiftFirst && m.out[i].PersonID == holder {
					m.out[i].Reason = reason
				}
			}
		}
	}
}

// fillRoamTeams staffs mixed-gender pairs: one door-set lead per team
// where possible, then a partner who makes the pair one male and one
// female. An unknown gender fits either half and flags the pair.
func (m *matcher) fillRoamTeams() {
	teams := map[string][]*MatchPost{}
	var keys []string
	for _, post := range m.postsFor(ShiftSecond) {
		if post.Pairing != PairingMixedGender {
			continue
		}
		k := post.Dept.String() + "|" + pairKey(post.Name)
		if _, ok := teams[k]; !ok {
			keys = append(keys, k)
		}
		teams[k] = append(teams[k], post)
	}
	sort.Strings(keys)

	// Door-set leads go first, most tenured first.
	var leads, others []*MatchPerson
	for _, id := range m.order {
		p := m.people[id]
		if !m.available(ShiftSecond, id) {
			continue
		}
		if post := m.posts[m.placed[ShiftFirst][id]]; post != nil && post.IsLead && post.Pattern == TwoShiftDoor {
			leads = append(leads, p)
		} else {
			others = append(others, p)
		}
	}
	sort.SliceStable(leads, func(i, j int) bool { return leads[i].Tenure > leads[j].Tenure })

	for _, k := range keys {
		team := teams[k]
		sort.Slice(team, func(i, j int) bool { return team[i].Name < team[j].Name })
		// Pinned members count towards the pair's gender rule.
		var members []*MatchPerson
		for id, post := range m.placed[ShiftSecond] {
			for _, tp := range team {
				if tp.ID == post {
					members = append(members, m.people[id])
				}
			}
		}
		for _, post := range team {
			for m.open(ShiftSecond, post) {
				// A team's first half is a door-set lead whenever one is
				// free; only then does anyone else start a team.
				pickFrom := func(cands []*MatchPerson) *MatchPerson {
					var best *MatchPerson
					for _, c := range cands {
						if !m.available(ShiftSecond, c.ID) || !m.eligible(c, post) || !pairs(members, c) {
							continue
						}
						if best == nil || m.betterPartner(c, best, members, post) {
							best = c
						}
					}
					return best
				}
				// Leads start teams; partners come from everyone else, so
				// each team gets one lead while leads last.
				var best *MatchPerson
				if len(members) == 0 {
					best = pickFrom(leads)
				} else {
					best = pickFrom(others)
				}
				if best == nil {
					best = pickFrom(append(append([]*MatchPerson(nil), leads...), others...))
				}
				if best == nil {
					break
				}
				reason := "roam partner"
				if len(members) == 0 {
					reason = "roam team half"
				}
				if post := m.posts[m.placed[ShiftFirst][best.ID]]; post != nil && post.IsLead {
					reason = "door-set lead leads the roam team"
				}
				members = append(members, best)
				m.place(ShiftSecond, post, best, reason, false)
			}
		}
		// Flag the pair when any member's gender is not recorded.
		if len(members) > 1 {
			for _, p := range members {
				if p.Gender == "unknown" {
					for i := range m.out {
						for _, q := range members {
							if m.out[i].Shift == ShiftSecond && m.out[i].PersonID == q.ID {
								m.out[i].NeedsConfirmation = true
							}
						}
					}
					break
				}
			}
		}
	}
}

// pairs reports whether c can join members in a one-male, one-female team.
func pairs(members []*MatchPerson, c *MatchPerson) bool {
	for _, p := range members {
		if p.Gender != "unknown" && p.Gender == c.Gender {
			return false
		}
	}
	return true
}

// betterPartner ranks roam candidates: the priority ladder first (opted
// in beats no preference), then a recorded gender over an unknown one for
// a second member (no confirmation needed), then tenure.
func (m *matcher) betterPartner(a, b *MatchPerson, members []*MatchPerson, post *MatchPost) bool {
	ra, _ := m.rung(a, post)
	rb, _ := m.rung(b, post)
	if ra != rb {
		return ra < rb
	}
	if len(members) > 0 && (a.Gender == "unknown") != (b.Gender == "unknown") {
		return b.Gender == "unknown"
	}
	if a.Tenure != b.Tenure {
		return a.Tenure > b.Tenure
	}
	return a.Badge < b.Badge
}

// pairKey groups the halves of a pair: "Roam Floor 1 Team 2 A" and
// "… B" share "Roam Floor 1 Team 2".
func pairKey(name string) string {
	f := strings.Fields(name)
	if n := len(f); n > 1 && len(f[n-1]) == 1 {
		return strings.Join(f[:n-1], " ")
	}
	return name
}
