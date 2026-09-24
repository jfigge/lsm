// Package entity holds the base types every LSM entity shares: the UUID
// identifier and the timestamp encoding used in storage and in archives.
package entity

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ID identifies every entity. It is a UUID — never an auto-increment — so
// that archives exported from one database import into another without
// collision. New IDs are version 7 (time-ordered), which keeps SQLite
// primary-key inserts roughly append-only; any valid UUID is accepted on
// parse, so IDs minted elsewhere (e.g. an ABI migration) import unchanged.
type ID struct{ u uuid.UUID }

// Nil is the zero ID. It is never a valid stored identifier.
var Nil ID

// NewID returns a fresh version-7 UUID.
func NewID() ID {
	u, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the system random source fails, at which
		// point nothing else in the process is trustworthy either.
		panic(fmt.Sprintf("entity: generating id: %v", err))
	}
	return ID{u}
}

// ParseID parses the canonical textual form of a UUID.
func ParseID(s string) (ID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return Nil, fmt.Errorf("entity: invalid id %q: %w", s, err)
	}
	return ID{u}, nil
}

// MustParseID is ParseID for constants and tests.
func MustParseID(s string) ID {
	id, err := ParseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// IsNil reports whether id is the zero ID.
func (id ID) IsNil() bool { return id.u == uuid.Nil }

// String returns the canonical lowercase form.
func (id ID) String() string { return id.u.String() }

// MarshalText implements encoding.TextMarshaler (and so JSON).
func (id ID) MarshalText() ([]byte, error) { return []byte(id.u.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler (and so JSON).
func (id *ID) UnmarshalText(b []byte) error {
	parsed, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Value implements driver.Valuer. IDs are stored as TEXT.
func (id ID) Value() (driver.Value, error) {
	if id.IsNil() {
		return nil, nil
	}
	return id.u.String(), nil
}

// Scan implements sql.Scanner.
func (id *ID) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = Nil
		return nil
	case string:
		return id.UnmarshalText([]byte(v))
	case []byte:
		return id.UnmarshalText(v)
	default:
		return fmt.Errorf("entity: cannot scan %T into ID", src)
	}
}

// NullID is an optional reference to another entity.
type NullID struct {
	ID    ID
	Valid bool
}

// Some wraps a present ID.
func Some(id ID) NullID { return NullID{ID: id, Valid: !id.IsNil()} }

// Value implements driver.Valuer.
func (n NullID) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.ID.Value()
}

// Scan implements sql.Scanner.
func (n *NullID) Scan(src any) error {
	if src == nil {
		*n = NullID{}
		return nil
	}
	if err := n.ID.Scan(src); err != nil {
		return err
	}
	n.Valid = true
	return nil
}

// MarshalJSON encodes an absent reference as null.
func (n NullID) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return []byte(`"` + n.ID.String() + `"`), nil
}

// UnmarshalJSON accepts null or a UUID string.
func (n *NullID) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = NullID{}
		return nil
	}
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return fmt.Errorf("entity: invalid id json %s", b)
	}
	if err := n.ID.UnmarshalText(b[1 : len(b)-1]); err != nil {
		return err
	}
	n.Valid = true
	return nil
}

// TimeLayout is the storage and archive encoding for instants: RFC 3339 in
// UTC with fixed-width fractional seconds, so TEXT columns sort correctly.
const TimeLayout = "2006-01-02T15:04:05.000000Z"

// DateLayout is the encoding for calendar dates (e.g. hire_date).
const DateLayout = "2006-01-02"

// FormatTime renders t in TimeLayout.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeLayout) }

// ParseTime parses a stored instant. RFC 3339 input with any offset is
// accepted so hand-edited archives still import.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("entity: invalid time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// FormatDate renders the calendar date of t in DateLayout.
func FormatDate(t time.Time) string { return t.Format(DateLayout) }

// ParseDate parses a stored calendar date as midnight UTC.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("entity: invalid date %q: %w", s, err)
	}
	return t, nil
}
