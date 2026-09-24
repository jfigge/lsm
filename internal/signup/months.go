package signup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"lsm/internal/entity"
	"lsm/internal/shifts"
	"lsm/internal/store"
)

// ErrNotFound is returned when a month or event does not exist or is not
// visible to the caller.
var ErrNotFound = errors.New("signup: not found")

// ErrMonthNotOpen rejects a signup change outside an open month.
var ErrMonthNotOpen = errors.New("signup: month is not open for signups")

// InputError is a request the caller got wrong, as opposed to a failure.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func invalidf(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// Notifier tells staff that a month has opened. The spec calls for email;
// the demo has no mail relay, so the default implementation logs.
type Notifier interface {
	MonthOpened(ctx context.Context, month string, recipients []string) error
}

// LogNotifier records what would have been emailed.
type LogNotifier struct{ Log *slog.Logger }

// MonthOpened logs the notification instead of sending it.
func (n LogNotifier) MonthOpened(_ context.Context, month string, recipients []string) error {
	n.Log.Info("month opened: email not sent (no mail relay configured)", "month", month, "recipients", len(recipients))
	return nil
}

// ParseMonth validates a YYYY-MM month.
func ParseMonth(s string) (time.Time, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return time.Time{}, invalidf("invalid month %q, want YYYY-MM", s)
	}
	return t, nil
}

// MonthSummary is a month as listed to staff and admins.
type MonthSummary struct {
	Month  string             `json:"month"`
	Status shifts.MonthStatus `json:"status"` // "" = not opened
	Events int                `json:"events"`
}

// ListMonths returns every month that has events or a status. Staff see
// only opened months; admins see all.
func ListMonths(ctx context.Context, q store.DBTX, includeUnopened bool) ([]MonthSummary, error) {
	rows, err := q.QueryContext(ctx, `
		WITH months AS (
			SELECT substr(event_date, 1, 7) AS month FROM events
			UNION SELECT month FROM schedule_months
		)
		SELECT m.month, COALESCE(s.status, ''),
		       (SELECT COUNT(*) FROM events e WHERE substr(e.event_date, 1, 7) = m.month)
		FROM months m LEFT JOIN schedule_months s ON s.month = m.month
		ORDER BY m.month`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonthSummary
	for rows.Next() {
		var m MonthSummary
		if err := rows.Scan(&m.Month, &m.Status, &m.Events); err != nil {
			return nil, err
		}
		if m.Status == "" && !includeUnopened {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MonthStatus returns a month's status, or "" if it has not been opened.
func MonthStatus(ctx context.Context, q store.DBTX, month string) (shifts.MonthStatus, error) {
	var s shifts.MonthStatus
	err := q.QueryRowContext(ctx, `SELECT status FROM schedule_months WHERE month = ?`, month).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

// SetMonthStatus opens or closes a month. Opening a month that was not
// already open emails every active person with a login.
func SetMonthStatus(ctx context.Context, db *sql.DB, month string, status shifts.MonthStatus,
	by entity.ID, now time.Time, n Notifier) error {
	if _, err := ParseMonth(month); err != nil {
		return err
	}
	if status != shifts.MonthOpen && status != shifts.MonthClosed {
		return invalidf("invalid month status %q", status)
	}
	var (
		previous   shifts.MonthStatus
		recipients []string
	)
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		var err error
		if previous, err = MonthStatus(ctx, tx, month); err != nil {
			return err
		}
		if previous == status {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_months (id, month, status, changed_at, changed_by)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (month) DO UPDATE SET
				status = excluded.status, changed_at = excluded.changed_at, changed_by = excluded.changed_by`,
			entity.NewID(), month, status, entity.FormatTime(now), by); err != nil {
			return err
		}
		if status != shifts.MonthOpen {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT email FROM persons WHERE active = 1 AND email IS NOT NULL ORDER BY email`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			recipients = append(recipients, e)
		}
		return rows.Err()
	})
	if err != nil || previous == status || status != shifts.MonthOpen || n == nil {
		return err
	}
	// Notification follows the commit: a failed email never un-opens a
	// month, and the admin can re-announce.
	return n.MonthOpened(ctx, month, recipients)
}
