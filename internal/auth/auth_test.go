package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lsm/internal/entity"
	"lsm/internal/staff"
	"lsm/internal/store"
)

func TestMain(m *testing.M) {
	Iterations = 1000 // keep the suite fast; the scheme is what is under test
	os.Exit(m.Run())
}

func TestPasswordHashes(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$1000$") {
		t.Fatalf("hash = %s", h)
	}
	if h2, _ := HashPassword("correct horse"); h2 == h {
		t.Error("hashes are not salted")
	}
	for pw, want := range map[string]bool{"correct horse": true, "Correct horse": false, "": false} {
		if ok, err := VerifyPassword(h, pw); err != nil || ok != want {
			t.Errorf("verify %q = %v, %v", pw, ok, err)
		}
	}
	sum := sha256.Sum256([]byte("jfigge"))
	legacy := "sha256:" + hex.EncodeToString(sum[:])
	if ok, _ := VerifyPassword(legacy, "jfigge"); !ok {
		t.Error("legacy seed hash does not verify")
	}
	if _, err := VerifyPassword("md5:abc", "x"); err == nil {
		t.Error("unknown scheme accepted")
	}
}

func setup(t *testing.T) (*sql.DB, entity.ID) {
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
	role, person := entity.NewID(), entity.NewID()
	hire, _ := entity.ParseDate("2024-08-24")
	sum := sha256.Sum256([]byte("jfigge"))
	for _, err := range []error{
		staff.InsertRole(ctx, db, staff.Role{ID: role, Name: "Event Security"}),
		staff.InsertPerson(ctx, db, staff.Person{ID: person, Badge: "100000", Name: "Jason Figge", RoleID: role,
			Tier: staff.TierAdmin, HireDate: hire, Gender: staff.GenderMale, Active: true}),
		staff.InsertCredential(ctx, db, staff.Credential{PersonID: person, Username: "jason.figge@lenovo.com",
			PasswordHash: "sha256:" + hex.EncodeToString(sum[:]), MustChangePassword: true}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return db, person
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	db, person := setup(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	if _, _, err := SignIn(ctx, db, "jason.figge@lenovo.com", "wrong", now); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := SignIn(ctx, db, "nobody@lenovo.com", "jfigge", now); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown user: %v", err)
	}
	// Usernames are case-insensitive.
	token, p, err := SignIn(ctx, db, " Jason.Figge@Lenovo.com", "jfigge", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.PersonID != person || !p.MustChangePassword || !p.IsAdmin() {
		t.Errorf("principal = %+v", p)
	}
	var stored string
	db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored)
	if stored == token || strings.Contains(stored, token) {
		t.Error("raw token stored")
	}

	if _, err := Authenticate(ctx, db, token, now.Add(SessionLifetime-time.Hour)); err != nil {
		t.Errorf("token rejected before expiry: %v", err)
	}
	// That use slid the expiry forward.
	if _, err := Authenticate(ctx, db, token, now.Add(SessionLifetime+time.Hour)); err != nil {
		t.Errorf("expiry did not slide: %v", err)
	}
	if _, err := Authenticate(ctx, db, token, now.Add(3*SessionLifetime)); !errors.Is(err, ErrNoSession) {
		t.Errorf("expired token accepted: %v", err)
	}

	// Changing the password clears the forced change and signs out every
	// other device.
	token, p, _ = SignIn(ctx, db, "jason.figge@lenovo.com", "jfigge", now)
	other, _, _ := SignIn(ctx, db, "jason.figge@lenovo.com", "jfigge", now)
	if err := ChangePassword(ctx, db, p, "jfigge", "short", now); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("short password: %v", err)
	}
	if err := ChangePassword(ctx, db, p, "jfigge", "jfigge", now); !errors.Is(err, ErrSamePassword) {
		t.Errorf("unchanged password: %v", err)
	}
	if err := ChangePassword(ctx, db, p, "nope", "a-new-password", now); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong current password: %v", err)
	}
	if err := ChangePassword(ctx, db, p, "jfigge", "a-new-password", now); err != nil {
		t.Fatal(err)
	}
	if p, err := Authenticate(ctx, db, token, now); err != nil || p.MustChangePassword {
		t.Errorf("this device after change: %+v, %v", p, err)
	}
	if _, err := Authenticate(ctx, db, other, now); !errors.Is(err, ErrNoSession) {
		t.Errorf("other device still signed in: %v", err)
	}
	var hash string
	db.QueryRow(`SELECT password_hash FROM credentials`).Scan(&hash)
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Errorf("legacy hash not replaced: %s", hash)
	}

	if err := SignOut(ctx, db, p.SessionID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Authenticate(ctx, db, token, now); !errors.Is(err, ErrNoSession) {
		t.Errorf("signed-out token accepted: %v", err)
	}
}
