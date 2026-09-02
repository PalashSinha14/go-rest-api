// Package httpx defines the JSON envelope every handler in the API responds with,
// so clients can rely on one shape regardless of which endpoint they hit.
package httpx

import "github.com/gin-gonic/gin"

// ErrorCode is a stable, machine-readable identifier for a failure. Clients should
// branch on these rather than on HTTP status or message text.
type ErrorCode string

const (
	CodeValidation   ErrorCode = "VALIDATION_ERROR"
	CodeUnauthorized ErrorCode = "UNAUTHORIZED"
	CodeForbidden    ErrorCode = "FORBIDDEN"
	CodeNotFound     ErrorCode = "NOT_FOUND"
	CodeConflict     ErrorCode = "CONFLICT"
	CodeInternal     ErrorCode = "INTERNAL_ERROR"
)

// FieldError describes a single field that failed validation.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Meta carries pagination details on list responses.
type Meta struct {
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// Success writes a successful envelope: {"status":"success","data":...}.
func Success(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"status": "success", "data": data})
}

// SuccessWithMeta writes a successful list envelope including pagination meta.
func SuccessWithMeta(c *gin.Context, status int, data any, meta Meta) {
	c.JSON(status, gin.H{"status": "success", "data": data, "meta": meta})
}

// Error writes an error envelope and aborts the handler chain, so no later
// handler can write a second body onto the same response.
func Error(c *gin.Context, status int, code ErrorCode, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"status":  "error",
		"code":    code,
		"message": message,
	})
}

// ValidationError writes a 422 listing every field that failed, rather than
// stopping at the first one, so a client can fix a whole form in one round trip.
func ValidationError(c *gin.Context, fields []FieldError) {
	c.AbortWithStatusJSON(422, gin.H{
		"status":  "error",
		"code":    CodeValidation,
		"message": "request validation failed",
		"errors":  fields,
	})
}
