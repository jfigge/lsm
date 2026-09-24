package signup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// The weighted ranking rules. Each scores a person 0 to 100 on its own
// terms; weights are data (ranking_rules) and a person's total is the
// weighted sum. Signup time is the unweighted tiebreak.
const (
	RuleAvailabilityRate  = "availability_rate"
	RulePerformanceRating = "performance_rating"
	RuleCapabilityMatch   = "capability_match"
	RuleSeasonLoad        = "season_load"
	RuleRecency           = "recency"
	RuleTenure            = "tenure"
)

// Rules lists every rule in display order.
var Rules = []string{
	RuleAvailabilityRate, RulePerformanceRating, RuleCapabilityMatch,
	RuleSeasonLoad, RuleRecency, RuleTenure,
}

// RuleDescriptions says, for each rule, what scores 100 and what scores 0.
var RuleDescriptions = map[string]string{
	RuleAvailabilityRate:  "100 at or above the availability expectation this season; 0 at half of it or below",
	RulePerformanceRating: "mean supervisor rating this season: exceptional 100, acceptable 50, needs discipline 0; unrated is 50",
	RuleCapabilityMatch:   "100 when the person holds a capability whose posts are all still unfilled; 0 when they hold nothing still needed",
	RuleSeasonLoad:        "100 when they have worked none of this season's events so far; 0 when they have worked all of them",
	RuleRecency:           "100 when they last worked 30 or more days ago (or never this season); 0 when they worked the day before",
	RuleTenure:            "100 at 3+ years; 0 when newly hired",
}

// MaxWeight bounds a single rule's weight.
const MaxWeight = 1000

// Weights maps rule name to weight. Weight 0 disables a rule.
type Weights map[string]int

// Validate checks every rule is present, known and in range.
func (w Weights) Validate() error {
	for name, v := range w {
		if _, ok := RuleDescriptions[name]; !ok {
			return invalidf("unknown rule %q", name)
		}
		if v < 0 || v > MaxWeight {
			return invalidf("rule %q: weight %d out of range 0..%d", name, v, MaxWeight)
		}
	}
	for _, name := range Rules {
		if _, ok := w[name]; !ok {
			return invalidf("rule %q has no weight", name)
		}
	}
	return nil
}

// Clone returns a copy of w.
func (w Weights) Clone() Weights {
	out := make(Weights, len(w))
	for k, v := range w {
		out[k] = v
	}
	return out
}

// LoadWeights returns the stored weights.
func LoadWeights(ctx context.Context, q store.DBTX) (Weights, error) {
	rows, err := q.QueryContext(ctx, `SELECT rule, weight FROM ranking_rules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	w := Weights{}
	for rows.Next() {
		var (
			name string
			v    int
		)
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		if _, ok := RuleDescriptions[name]; ok {
			w[name] = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range Rules {
		if _, ok := w[name]; !ok {
			w[name] = 0 // a rule added in code but not yet weighted is off
		}
	}
	return w, nil
}

// SaveWeights stores new weights, recording who changed them. Partial
// updates are merged over the stored weights.
func SaveWeights(ctx context.Context, db *sql.DB, update Weights, by entity.ID, now time.Time) (Weights, error) {
	var out Weights
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		w, err := LoadWeights(ctx, tx)
		if err != nil {
			return err
		}
		for k, v := range update {
			w[k] = v
		}
		if err := w.Validate(); err != nil {
			return err
		}
		for i, name := range Rules {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ranking_rules (id, rule, weight, sort_order, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (rule) DO UPDATE SET
					weight = excluded.weight, updated_at = excluded.updated_at, updated_by = excluded.updated_by
				WHERE ranking_rules.weight <> excluded.weight`,
				entity.NewID(), name, w[name], i+1, entity.FormatTime(now), by); err != nil {
				return err
			}
		}
		out = w
		return nil
	})
	return out, err
}

// DefaultExpectation is the arena's published policy: staff are expected
// to sign up for at least 60% of events each month.
const DefaultExpectation = 0.60

const expectationKey = "availability_expectation"

// Expectation returns the admin-configured availability expectation, as a
// fraction.
func Expectation(ctx context.Context, q store.DBTX) (float64, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, expectationKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultExpectation, nil
	}
	if err != nil {
		return 0, err
	}
	var v float64
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return 0, fmt.Errorf("signup: setting %s: %w", expectationKey, err)
	}
	return v, nil
}

// SetExpectation stores the availability expectation (0 < v <= 1).
func SetExpectation(ctx context.Context, q store.DBTX, v float64, now time.Time) error {
	if v <= 0 || v > 1 {
		return invalidf("expectation %.2f must be above 0 and at most 1", v)
	}
	b, _ := json.Marshal(v)
	_, err := q.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		expectationKey, string(b), entity.FormatTime(now))
	return err
}
