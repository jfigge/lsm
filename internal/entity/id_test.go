package entity

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewIDIsVersion7AndUnique(t *testing.T) {
	seen := make(map[ID]bool)
	for range 10000 {
		id := NewID()
		if id.u.Version() != 7 {
			t.Fatalf("version = %d, want 7", id.u.Version())
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}

func TestIDRoundTrip(t *testing.T) {
	id := NewID()

	b, err := json.Marshal(struct{ ID ID }{id})
	if err != nil {
		t.Fatal(err)
	}
	var back struct{ ID ID }
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != id {
		t.Fatalf("json round trip: got %s want %s", back.ID, id)
	}

	v, _ := id.Value()
	var scanned ID
	if err := scanned.Scan(v); err != nil || scanned != id {
		t.Fatalf("sql round trip: got %s, %v", scanned, err)
	}
}

func TestParseIDRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "42", "not-a-uuid"} {
		if _, err := ParseID(s); err == nil {
			t.Errorf("ParseID(%q) succeeded", s)
		}
	}
}

func TestNullIDJSON(t *testing.T) {
	var n NullID
	if err := json.Unmarshal([]byte("null"), &n); err != nil || n.Valid {
		t.Fatalf("null: %+v, %v", n, err)
	}
	id := NewID()
	b, _ := json.Marshal(Some(id))
	if err := json.Unmarshal(b, &n); err != nil || !n.Valid || n.ID != id {
		t.Fatalf("value: %+v, %v", n, err)
	}
}

func TestTimeLayoutSortsLexically(t *testing.T) {
	a := time.Date(2026, 9, 29, 19, 0, 0, 0, time.UTC)
	b := a.Add(time.Microsecond)
	if !(FormatTime(a) < FormatTime(b)) {
		t.Fatalf("%s !< %s", FormatTime(a), FormatTime(b))
	}
	got, err := ParseTime(FormatTime(b))
	if err != nil || !got.Equal(b) {
		t.Fatalf("ParseTime: %v, %v", got, err)
	}
}
