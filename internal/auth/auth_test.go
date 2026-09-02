package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/models"
)

func testUser() *models.User {
	return &models.User{
		ID:    bson.NewObjectID(),
		Email: "reader@example.com",
		Role:  models.RoleMember,
	}
}

func TestGenerateAndParseRoundTrip(t *testing.T) {
	tm := NewTokenManager("a-sufficiently-long-test-secret", time.Hour)
	user := testUser()

	token, expiresAt, err := tm.Generate(user)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Errorf("expiresAt = %v, want a future time", expiresAt)
	}

	claims, err := tm.Parse(token)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if claims.UserID != user.ID.Hex() {
		t.Errorf("UserID = %q, want %q", claims.UserID, user.ID.Hex())
	}
	if claims.Role != models.RoleMember {
		t.Errorf("Role = %q, want %q", claims.Role, models.RoleMember)
	}
	if claims.Email != user.Email {
		t.Errorf("Email = %q, want %q", claims.Email, user.Email)
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	tm := NewTokenManager("a-sufficiently-long-test-secret", time.Hour)
	// Issue the token an hour and a half in the past so its one-hour TTL has run out.
	tm.now = func() time.Time { return time.Now().Add(-90 * time.Minute) }

	token, _, err := tm.Generate(testUser())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	tm.now = time.Now
	if _, err := tm.Parse(token); err != ErrTokenExpired {
		t.Errorf("Parse() error = %v, want ErrTokenExpired", err)
	}
}

func TestParseRejectsTokenSignedWithAnotherSecret(t *testing.T) {
	issuer := NewTokenManager("the-original-signing-secret", time.Hour)
	token, _, err := issuer.Generate(testUser())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	attacker := NewTokenManager("a-completely-different-secret", time.Hour)
	if _, err := attacker.Parse(token); err == nil {
		t.Fatal("Parse() accepted a token signed with a different secret")
	}
}

// TestParseRejectsAlgNone guards the classic JWT downgrade: a token whose header
// claims alg=none has no signature to verify, and a parser that trusts the header
// will accept anything the caller writes into the claims.
func TestParseRejectsAlgNone(t *testing.T) {
	claims := &Claims{
		UserID: bson.NewObjectID().Hex(),
		Role:   models.RoleLibrarian,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("constructing unsigned token: %v", err)
	}

	tm := NewTokenManager("a-sufficiently-long-test-secret", time.Hour)
	if _, err := tm.Parse(unsigned); err == nil {
		t.Fatal("Parse() accepted an unsigned alg=none token")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	tm := NewTokenManager("a-sufficiently-long-test-secret", time.Hour)
	for _, token := range []string{"", "not.a.token", "aaa.bbb.ccc"} {
		if _, err := tm.Parse(token); err == nil {
			t.Errorf("Parse(%q) accepted a malformed token", token)
		}
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	const plain = "correct-horse-battery-staple"

	hash, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if hash == plain {
		t.Fatal("HashPassword() returned the plaintext")
	}
	if !CheckPassword(hash, plain) {
		t.Error("CheckPassword() rejected the correct password")
	}
	if CheckPassword(hash, "the-wrong-password") {
		t.Error("CheckPassword() accepted an incorrect password")
	}
}

// TestHashPasswordIsSalted checks that identical passwords hash differently, so a
// leaked database cannot be attacked by grouping identical hashes.
func TestHashPasswordIsSalted(t *testing.T) {
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if first == second {
		t.Error("identical passwords produced identical hashes; salt is missing")
	}
}

func TestHashPasswordRejectsOverlongInput(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("x", MaxPasswordBytes+1)); err != ErrPasswordTooLong {
		t.Errorf("HashPassword() error = %v, want ErrPasswordTooLong", err)
	}
}
