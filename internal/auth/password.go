// Package auth handles sign-in: password hashing, and the long-lived
// session tokens the app keeps in the iOS Keychain (SPEC §10.1).
//
// Account machinery is deliberately minimal: no self-service reset, no
// onboarding. Admin sets credentials; seeded ones force a change at first
// sign-in.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Iterations is the PBKDF2-SHA256 work factor (OWASP 2023 guidance).
// Tests lower it.
var Iterations = 600_000

const schemePBKDF2 = "pbkdf2-sha256"

// MinPasswordLength is the shortest password accepted on change.
const MinPasswordLength = 8

// ErrWeakPassword rejects a new password that is too short.
var ErrWeakPassword = fmt.Errorf("auth: password must be at least %d characters", MinPasswordLength)

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<key>".
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", schemePBKDF2, Iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches stored. Besides the
// native scheme it accepts the seed's legacy "sha256:<hex>" (unsalted)
// form, which is only ever paired with a forced password change.
func VerifyPassword(stored, password string) (bool, error) {
	if hexSum, ok := strings.CutPrefix(stored, "sha256:"); ok {
		want, err := hex.DecodeString(hexSum)
		if err != nil {
			return false, fmt.Errorf("auth: malformed legacy hash")
		}
		got := sha256.Sum256([]byte(password))
		return hmac.Equal(got[:], want), nil
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != schemePBKDF2 {
		return false, errors.New("auth: unknown password hash scheme")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false, errors.New("auth: malformed password hash")
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false, errors.New("auth: malformed password hash")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false, err
	}
	return hmac.Equal(got, want), nil
}
