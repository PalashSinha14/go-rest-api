package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ErrPasswordTooLong is returned when a password exceeds bcrypt's hard limit.
// bcrypt silently truncates at 72 bytes in some implementations; Go's returns an
// error instead, and we surface it rather than storing a hash of a prefix.
var ErrPasswordTooLong = errors.New("password exceeds 72 bytes")

// MaxPasswordBytes is bcrypt's maximum input length.
const MaxPasswordBytes = 72

// HashPassword returns a bcrypt hash of plain at the default cost.
func HashPassword(plain string) (string, error) {
	if len(plain) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword reports whether plain matches hash.
//
// It returns a bool rather than an error because callers must not branch on why
// a comparison failed: telling "no such user" apart from "wrong password" lets an
// attacker enumerate registered email addresses.
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
