// Package middleware holds the cross-cutting request handling: correlation IDs,
// structured access logs, panic recovery, JWT authentication and body validation.
package middleware

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/httpx"
)

// payloadKey is where a validated body is stashed for the handler to collect.
const payloadKey = "validated_payload"

// RegisterValidators teaches gin's validator the custom rules our models use. It
// is called once at startup; the engine is process-global.
func RegisterValidators() error {
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		return fmt.Errorf("gin validator engine is not *validator.Validate")
	}

	// Report the JSON field name in errors. Without this a client sees "BookID"
	// when what they sent was "book_id", and cannot map the error to their input.
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	if err := v.RegisterValidation("mongoid", validateMongoID); err != nil {
		return fmt.Errorf("register mongoid validator: %w", err)
	}
	return nil
}

// validateMongoID accepts strings that parse as a MongoDB ObjectID, so a
// malformed id is rejected at the edge with a field error rather than reaching a
// repository and coming back as a generic 500.
func validateMongoID(fl validator.FieldLevel) bool {
	s, ok := fl.Field().Interface().(string)
	if !ok {
		return false
	}
	_, err := bson.ObjectIDFromHex(s)
	return err == nil
}

// ValidateJSON decodes and validates a request body into T, aborting with a 422
// listing every offending field if it does not conform.
//
// Handlers then read the result with Payload[T], so no handler in the codebase
// repeats bind-and-check boilerplate or forgets to check the error.
func ValidateJSON[T any]() gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload T

		if err := c.ShouldBindJSON(&payload); err != nil {
			var verrs validator.ValidationErrors
			switch {
			case errors.As(err, &verrs):
				httpx.ValidationError(c, fieldErrors(verrs))
			case errors.Is(err, io.EOF):
				httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
					"request body is empty")
			default:
				httpx.Error(c, http.StatusBadRequest, httpx.CodeValidation,
					"request body is not valid JSON")
			}
			return
		}

		c.Set(payloadKey, payload)
		c.Next()
	}
}

// Payload returns the body validated by ValidateJSON[T] earlier in the chain.
func Payload[T any](c *gin.Context) T {
	v, ok := c.Get(payloadKey)
	if !ok {
		var zero T
		return zero
	}
	payload, _ := v.(T)
	return payload
}

// fieldErrors turns validator's output into the API's error shape.
func fieldErrors(verrs validator.ValidationErrors) []httpx.FieldError {
	out := make([]httpx.FieldError, 0, len(verrs))
	for _, fe := range verrs {
		out = append(out, httpx.FieldError{
			Field:   fe.Field(),
			Message: describe(fe),
		})
	}
	return out
}

// describe renders one validation failure as a sentence a client can show a user.
func describe(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "this field is required"
	case "email":
		return "must be a valid email address"
	case "isbn":
		return "must be a valid ISBN-10 or ISBN-13"
	case "mongoid":
		return "must be a valid id"
	case "min":
		if fe.Kind() == reflect.String {
			return fmt.Sprintf("must be at least %s characters", fe.Param())
		}
		return fmt.Sprintf("must be at least %s", fe.Param())
	case "max":
		if fe.Kind() == reflect.String {
			return fmt.Sprintf("must be at most %s characters", fe.Param())
		}
		return fmt.Sprintf("must be at most %s", fe.Param())
	case "oneof":
		return fmt.Sprintf("must be one of: %s", strings.ReplaceAll(fe.Param(), " ", ", "))
	default:
		return fmt.Sprintf("failed the %q rule", fe.Tag())
	}
}
