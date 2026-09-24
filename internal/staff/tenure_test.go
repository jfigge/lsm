package staff

import (
	"testing"
	"time"
)

func TestTenure(t *testing.T) {
	d := func(s string) time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	hire := d("2024-08-24")
	cases := []struct {
		asOf string
		want HalfYears
		band string
	}{
		{"2024-08-24", 0, "0"},
		{"2025-02-23", 0, "0"}, // one day short of six months
		{"2025-02-24", 1, "0.5"},
		{"2025-08-24", 2, "1"},
		{"2026-02-24", 3, "1.5"},
		{"2026-09-23", 4, "2"},
		{"2027-08-24", 6, "3+"},
		{"2035-01-01", 6, "3+"}, // capped
		{"2020-01-01", 0, "0"},  // hired in the future
	}
	for _, c := range cases {
		got := Tenure(hire, d(c.asOf))
		if got != c.want || got.String() != c.band {
			t.Errorf("Tenure(%s) = %d %q, want %d %q", c.asOf, got, got, c.want, c.band)
		}
	}
}
