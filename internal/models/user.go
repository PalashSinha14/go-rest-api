package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Role determines which endpoints a user may reach. Roles are hierarchical only
// in the sense that a librarian may do everything a member may do; the auth
// middleware encodes that explicitly rather than relying on ordering.
type Role string

const (
	// RoleMember can browse books and manage their own loans.
	RoleMember Role = "member"
	// RoleLibrarian can additionally manage the catalogue and view all loans.
	RoleLibrarian Role = "librarian"
)

// Valid reports whether r is a role the system recognises.
func (r Role) Valid() bool {
	return r == RoleMember || r == RoleLibrarian
}

// User is a library account. PasswordHash is never serialised to JSON.
type User struct {
	ID           bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Name         string        `bson:"name"          json:"name"`
	Email        string        `bson:"email"         json:"email"`
	PasswordHash string        `bson:"password_hash" json:"-"`
	Role         Role          `bson:"role"          json:"role"`
	CreatedAt    time.Time     `bson:"created_at"    json:"created_at"`
	UpdatedAt    time.Time     `bson:"updated_at"    json:"updated_at"`
}

// RegisterRequest is the body of POST /auth/register.
//
// Role is deliberately absent: allowing a caller to pick their own role would let
// anyone self-promote to librarian. New accounts are always members, and an
// existing librarian promotes them via PATCH /users/:id/role.
type RegisterRequest struct {
	Name     string `json:"name"     binding:"required,min=2,max=100"`
	Email    string `json:"email"    binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// UpdateRoleRequest is the body of PATCH /users/:id/role.
type UpdateRoleRequest struct {
	Role Role `json:"role" binding:"required,oneof=member librarian"`
}

// AuthResponse is returned by both register and login.
type AuthResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	User      *User  `json:"user"`
}
