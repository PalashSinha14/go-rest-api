package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/auth"
	"go-server/internal/httpx"
	"go-server/internal/middleware"
	"go-server/internal/models"
	"go-server/internal/repository"
)

// AuthHandler serves registration, login and account inspection.
type AuthHandler struct {
	users  repository.UserRepository
	tokens *auth.TokenManager
}

// NewAuthHandler wires an AuthHandler to its dependencies.
func NewAuthHandler(users repository.UserRepository, tokens *auth.TokenManager) *AuthHandler {
	return &AuthHandler{users: users, tokens: tokens}
}

// dummyHash is a valid bcrypt hash of a random value, compared against when no
// account matches so that login takes the same time whether or not the email
// exists. Skipping the comparison would turn response latency into an oracle for
// which addresses are registered.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// Register creates a member account and returns a token for it.
//
// The very first account to register becomes a librarian: a fresh deployment
// otherwise has no one who can add books, and there is no separate seeding step.
// Every account after that is a member, promoted only by an existing librarian.
func (h *AuthHandler) Register(c *gin.Context) {
	req := middleware.Payload[models.RegisterRequest](c)

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondRepoError(c, err, "")
		return
	}

	count, err := h.users.Count(c.Request.Context())
	if err != nil {
		respondRepoError(c, err, "")
		return
	}

	role := models.RoleMember
	if count == 0 {
		role = models.RoleLibrarian
	}

	user := &models.User{
		Name:         req.Name,
		Email:        req.Email,
		PasswordHash: hash,
		Role:         role,
	}

	if err := h.users.Create(c.Request.Context(), user); err != nil {
		respondRepoError(c, err, "")
		return
	}

	middleware.LoggerOf(c).Info("user registered",
		slog.String("user_id", user.ID.Hex()),
		slog.String("role", string(user.Role)),
	)

	h.respondWithToken(c, http.StatusCreated, user)
}

// Login exchanges credentials for a token.
func (h *AuthHandler) Login(c *gin.Context) {
	req := middleware.Payload[models.LoginRequest](c)

	user, err := h.users.FindByEmail(c.Request.Context(), req.Email)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		respondRepoError(c, err, "")
		return
	}

	// One message and one code path for both "no such account" and "wrong
	// password", so neither the response nor its timing reveals which it was.
	hash := dummyHash
	if user != nil {
		hash = user.PasswordHash
	}
	if !auth.CheckPassword(hash, req.Password) || user == nil {
		middleware.LoggerOf(c).Warn("login failed", slog.String("email", req.Email))
		httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized,
			"invalid email or password")
		return
	}

	middleware.LoggerOf(c).Info("user logged in", slog.String("user_id", user.ID.Hex()))
	h.respondWithToken(c, http.StatusOK, user)
}

// Me returns the authenticated caller's own account.
func (h *AuthHandler) Me(c *gin.Context) {
	id, ok := middleware.UserObjectIDOf(c)
	if !ok {
		httpx.Error(c, http.StatusUnauthorized, httpx.CodeUnauthorized,
			"authentication required")
		return
	}

	user, err := h.users.FindByID(c.Request.Context(), id)
	if err != nil {
		respondRepoError(c, err, "account not found")
		return
	}
	httpx.Success(c, http.StatusOK, user)
}

// UpdateRole promotes or demotes an account. Librarian-only.
func (h *AuthHandler) UpdateRole(c *gin.Context) {
	id, err := bson.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
			"user id is not a valid id")
		return
	}

	req := middleware.Payload[models.UpdateRoleRequest](c)

	// Refuse self-demotion: the last librarian removing their own privileges
	// would leave the catalogue unmanageable with no way back in.
	if middleware.UserIDOf(c) == id.Hex() && req.Role != models.RoleLibrarian {
		httpx.Error(c, http.StatusForbidden, httpx.CodeForbidden,
			"you cannot remove your own librarian role")
		return
	}

	user, err := h.users.UpdateRole(c.Request.Context(), id, req.Role)
	if err != nil {
		respondRepoError(c, err, "account not found")
		return
	}

	middleware.LoggerOf(c).Info("role updated",
		slog.String("target_user_id", user.ID.Hex()),
		slog.String("new_role", string(user.Role)),
	)
	httpx.Success(c, http.StatusOK, user)
}

// respondWithToken issues a token for user and writes the auth envelope.
func (h *AuthHandler) respondWithToken(c *gin.Context, status int, user *models.User) {
	token, expiresAt, err := h.tokens.Generate(user)
	if err != nil {
		respondRepoError(c, err, "")
		return
	}
	httpx.Success(c, status, models.AuthResponse{
		Token:     token,
		ExpiresAt: expiresAt.Unix(),
		User:      user,
	})
}
