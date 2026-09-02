package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/auth"
	"go-server/internal/httpx"
	"go-server/internal/models"
)

// Context keys holding the authenticated caller's identity.
const (
	claimsKey = "claims"
	userIDKey = "user_id"
	roleKey   = "user_role"
)

// RequireAuth rejects any request without a valid bearer token and puts the
// caller's claims on the context for downstream handlers.
func RequireAuth(tm *auth.TokenManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := bearerToken(c)
		if err != nil {
			httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized, err.Error())
			return
		}

		claims, err := tm.Parse(token)
		if err != nil {
			// The reason is logged but not returned: a client only needs to know
			// to sign in again, and detail here helps an attacker probe tokens.
			LoggerOf(c).Warn("token rejected", "reason", err.Error())

			msg := "invalid or malformed token"
			if errors.Is(err, auth.ErrTokenExpired) {
				msg = "token has expired"
			}
			httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized, msg)
			return
		}

		c.Set(claimsKey, claims)
		c.Set(userIDKey, claims.UserID)
		c.Set(roleKey, claims.Role)
		c.Next()
	}
}

// RequireRole allows the request through only if the caller holds one of the
// given roles. It must be installed after RequireAuth.
//
// Scoping is explicit rather than hierarchical: an endpoint that both members and
// librarians may call lists both. Ordering roles by "power" and comparing with >=
// is how privilege bugs get written.
func RequireRole(allowed ...models.Role) gin.HandlerFunc {
	permitted := make(map[models.Role]struct{}, len(allowed))
	for _, r := range allowed {
		permitted[r] = struct{}{}
	}

	return func(c *gin.Context) {
		role, ok := RoleOf(c)
		if !ok {
			httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized,
				"authentication required")
			return
		}
		if _, allowed := permitted[role]; !allowed {
			httpx.Error(c, http.StatusForbidden, httpx.CodeForbidden,
				"your role does not permit this action")
			return
		}
		c.Next()
	}
}

// bearerToken pulls the credential out of the Authorization header.
func bearerToken(c *gin.Context) (string, error) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return "", errors.New("authorization header is missing")
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", errors.New(`authorization header must be "Bearer <token>"`)
	}
	return strings.TrimSpace(token), nil
}

// ClaimsOf returns the authenticated caller's token claims.
func ClaimsOf(c *gin.Context) (*auth.Claims, bool) {
	v, ok := c.Get(claimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*auth.Claims)
	return claims, ok
}

// UserIDOf returns the caller's user id as a hex string, or "" if unauthenticated.
func UserIDOf(c *gin.Context) string {
	v, ok := c.Get(userIDKey)
	if !ok {
		return ""
	}
	id, _ := v.(string)
	return id
}

// UserObjectIDOf returns the caller's user id decoded for use in a query.
func UserObjectIDOf(c *gin.Context) (bson.ObjectID, bool) {
	id := UserIDOf(c)
	if id == "" {
		return bson.NilObjectID, false
	}
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return bson.NilObjectID, false
	}
	return oid, true
}

// RoleOf returns the caller's role.
func RoleOf(c *gin.Context) (models.Role, bool) {
	v, ok := c.Get(roleKey)
	if !ok {
		return "", false
	}
	role, ok := v.(models.Role)
	return role, ok
}

// IsLibrarian reports whether the caller holds the librarian role.
func IsLibrarian(c *gin.Context) bool {
	role, ok := RoleOf(c)
	return ok && role == models.RoleLibrarian
}
