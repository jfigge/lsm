package checkin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/store"
)

// RescanGrace is how long after checking in a second scan still reads as
// "already checked in" rather than checking out: a double scan at the
// door must not send someone home.
const RescanGrace = 10 * time.Minute

// Kiosk outcomes. The kiosk shows exactly one of these, then resets.
const (
	OutcomeUnknownBadge = "unknown_badge"
	OutcomeCheckedIn    = "checked_in"
	OutcomeAlreadyIn    = "already_in"
	OutcomeCheckedOut   = "checked_out"
	OutcomeReturnFirst  = "return_first"
	OutcomeAlreadyOut   = "already_out"
)

// KioskResult is what the kiosk screen shows after a scan. It is never
// blank: every path ends in a heading and a message.
type KioskResult struct {
	Outcome string      `json:"outcome"`
	Heading string      `json:"heading"`
	Message string      `json:"message"`
	Person  *PersonCard `json:"person,omitempty"`
	Time    string      `json:"time,omitempty"`
	Post    *Post       `json:"post,omitempty"`
	// SeeReception marks results that need the person to go to reception.
	SeeReception bool `json:"see_reception"`
}

// KioskScan handles one badge scan at a kiosk: check in on the first scan,
// check out on a later one.
func KioskScan(ctx context.Context, db *sql.DB, eventID entity.ID, badge string, station entity.ID,
	now time.Time, loc *time.Location) (KioskResult, error) {
	var res KioskResult
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		personID, err := ResolveBadge(ctx, tx, badge)
		if errors.Is(err, ErrBadgeUnknown) {
			res = KioskResult{Outcome: OutcomeUnknownBadge, Heading: "Badge not recognised",
				Message: "See reception", SeeReception: true}
			return nil
		}
		if err != nil {
			return err
		}
		d, err := DeskFor(ctx, tx, eventID, personID, loc)
		if err != nil {
			return err
		}
		res.Person, res.Post = &d.Person, d.Post
		where := Where{Location: "kiosk", Station: entity.Some(station)}

		switch {
		case d.Visit == nil:
			if _, err := CheckIn(ctx, tx, eventID, personID, where, now); err != nil {
				return err
			}
			res.Outcome, res.Heading, res.Time = OutcomeCheckedIn, "Checked in", clock(now, loc)
			res.Message, res.SeeReception = arrivalMessage(d)
		case d.Visit.CheckedOutAt != nil:
			res.Outcome, res.Heading, res.Time = OutcomeAlreadyOut, "Already checked out", d.Visit.CheckedOut
			res.Message, res.SeeReception = "See reception if you are coming back in", true
		case now.Sub(d.Visit.CheckedInAt) < RescanGrace:
			res.Outcome, res.Heading, res.Time = OutcomeAlreadyIn, "Checked in at "+d.Visit.CheckedIn, d.Visit.CheckedIn
			res.Message, res.SeeReception = arrivalMessage(d)
		default:
			var out []string
			for _, x := range d.Issued {
				if x.ReturnedAt == nil {
					out = append(out, strings.TrimSpace(x.Resource+" "+x.Number))
				}
			}
			if len(out) > 0 {
				res.Outcome, res.Heading = OutcomeReturnFirst, "Please check out at reception"
				res.Message, res.SeeReception = "Return "+strings.Join(out, ", ")+" before you leave", true
				return nil
			}
			if err := CheckOut(ctx, tx, eventID, personID, where, now); err != nil {
				return err
			}
			res.Outcome, res.Heading, res.Time = OutcomeCheckedOut, "Checked out", clock(now, loc)
			res.Message = "Thank you — good night"
		}
		return nil
	})
	return res, err
}

// arrivalMessage is the after-check-in instruction: go to the post, or
// see reception for resources, a missing post or a missing roster place.
func arrivalMessage(d Desk) (string, bool) {
	if d.Post == nil {
		for _, n := range d.Notices {
			if n == "Not on tonight's roster" {
				return "Not on tonight's roster — see reception", true
			}
		}
		return "No assignment — see reception", true
	}
	for _, f := range d.Fields {
		if !f.Required {
			continue
		}
		held := false
		for _, x := range d.Issued {
			if x.ResourceID == f.ID && x.ReturnedAt == nil {
				held = true
			}
		}
		if !held {
			return "Resources required — see reception", true
		}
	}
	where := d.Post.Name
	if d.Post.Area != "" && !strings.HasPrefix(d.Post.Name, d.Post.Area) {
		where = fmt.Sprintf("%s (%s)", d.Post.Name, d.Post.Area)
	}
	return "Go to your post: " + where, false
}

// TodaysEvents lists events on the venue's calendar date.
func TodaysEvents(ctx context.Context, q store.DBTX, now time.Time, loc *time.Location) ([]shifts.Event, error) {
	events, err := shifts.ListEvents(ctx, q)
	if err != nil {
		return nil, err
	}
	today := now.In(loc).Format("2006-01-02")
	out := []shifts.Event{}
	for _, e := range events {
		if entity.FormatDate(e.Date) == today {
			out = append(out, e)
		}
	}
	return out, nil
}
