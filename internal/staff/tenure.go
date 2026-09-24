package staff

import (
	"strconv"
	"time"
)

// HalfYears is general arena experience in completed half-years, capped at
// MaxTenure, which displays as "3+". It is the unit of both a person's
// tenure and a position's min_tenure.
type HalfYears int

// MaxTenure is the top band: three years or more.
const MaxTenure HalfYears = 6

// Tenure returns the completed half-years between hire and asOf, in the
// bands 0, 0.5 … 2.5, 3+. A half-year completes on the same day of the
// month six months on; a future hire date is 0.
func Tenure(hire, asOf time.Time) HalfYears {
	hy, hm, hd := hire.Date()
	ay, am, ad := asOf.Date()
	months := (ay-hy)*12 + int(am-hm)
	if ad < hd {
		months--
	}
	h := HalfYears(months / 6)
	switch {
	case h < 0:
		return 0
	case h > MaxTenure:
		return MaxTenure
	}
	return h
}

// String renders the band as displayed: "0", "0.5" … "2.5", "3+".
func (h HalfYears) String() string {
	if h >= MaxTenure {
		return "3+"
	}
	if h <= 0 {
		return "0"
	}
	s := strconv.Itoa(int(h) / 2)
	if h%2 == 1 {
		s += ".5"
	}
	return s
}
