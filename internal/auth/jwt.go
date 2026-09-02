// Package auth issues and verifies the JSON Web Tokens that carry a caller's
// identity and role, and hashes the passwords those identities are proven with.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/models"
)

// Errors returned by ParseToken. Handlers map all of them to 401 but the
// distinction is useful in logs when diagnosing a client that cannot stay signed in.
var (
	ErrTokenExpired = errors.New("token has expired")
	ErrTokenInvalid = errors.New("token is invalid")
)

const issuer = "library-inventory-api"

// Claims is the payload carried inside an access token. Role travels in the token
// so authorising a request costs no database round trip; the trade-off is that a
// role change only takes effect once the holder's current token expires.
type Claims struct {
	UserID string      `json:"uid"`
	Email  string      `json:"email"`
	Role   models.Role `json:"role"`
	jwt.RegisteredClaims
}

// TokenManager signs and verifies tokens with a single HMAC secret.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // injectable so tests can control expiry
}

// NewTokenManager builds a manager signing with secret and issuing tokens valid
// for ttl.
func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// Generate returns a signed token for u along with the instant it expires.
func (m *TokenManager) Generate(u *models.User) (string, time.Time, error) {
	issuedAt := m.now()
	expiresAt := issuedAt.Add(m.ttl)

	claims := &Claims{
		UserID: u.ID.Hex(),
		Email:  u.Email,
		Role:   u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.Hex(),
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Parse verifies a token's signature and expiry and returns its claims.
//
// The signing method is pinned to HMAC: without that check an attacker could
// present a token signed with "none", or with RS256 using our secret as a public
// key, and have it accepted.
func (m *TokenManager) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}

	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %q", t.Header["alg"])
		}
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
	)

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %s", ErrTokenInvalid, err)
	}

	if _, err := bson.ObjectIDFromHex(claims.UserID); err != nil {
		return nil, fmt.Errorf("%w: subject is not a valid id", ErrTokenInvalid)
	}
	return claims, nil
}

// ObjectID returns the caller's user id decoded from the token subject.
func (c *Claims) ObjectID() (bson.ObjectID, error) {
	return bson.ObjectIDFromHex(c.UserID)
}
