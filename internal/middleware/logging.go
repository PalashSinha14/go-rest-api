package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"go-server/internal/httpx"
)

// Context keys and the header used to carry a correlation id across services.
const (
	requestIDKey    = "request_id"
	loggerKey       = "logger"
	RequestIDHeader = "X-Request-ID"
)

// RequestID attaches a correlation id to every request, reusing an inbound
// X-Request-ID when the caller supplied one so a trace survives across hops, and
// echoing it back so a client can quote it in a bug report.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		c.Set(requestIDKey, id)
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A duplicate id degrades tracing but must never fail the request.
		return "req-" + time.Now().UTC().Format("20060102150405.000000")
	}
	return hex.EncodeToString(b[:])
}

// RequestIDOf returns the correlation id assigned to this request.
func RequestIDOf(c *gin.Context) string {
	id, _ := c.Get(requestIDKey)
	s, _ := id.(string)
	return s
}

// Logger emits one structured log line per request and puts a request-scoped
// logger on the context, so anything a handler logs is automatically correlated
// to the request that caused it.
//
// This replaces Gin's default text logger, which cannot be parsed reliably by a
// log aggregator.
func Logger(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		reqLog := base.With(
			slog.String("request_id", RequestIDOf(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		)
		c.Set(loggerKey, reqLog)

		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.Int("bytes", c.Writer.Size()),
			slog.String("client_ip", c.ClientIP()),
		}
		if q := c.Request.URL.RawQuery; q != "" {
			attrs = append(attrs, slog.String("query", q))
		}
		if uid := UserIDOf(c); uid != "" {
			attrs = append(attrs, slog.String("user_id", uid))
		}
		if errs := c.Errors.ByType(gin.ErrorTypePrivate); len(errs) > 0 {
			attrs = append(attrs, slog.String("error", errs.String()))
		}

		// Severity follows the response class so that alerting on "error" is
		// meaningful: a 404 is the client's problem, a 500 is ours.
		switch {
		case status >= http.StatusInternalServerError:
			reqLog.Error("request failed", attrs...)
		case status >= http.StatusBadRequest:
			reqLog.Warn("request rejected", attrs...)
		default:
			reqLog.Info("request completed", attrs...)
		}
	}
}

// LoggerOf returns the request-scoped logger, falling back to the default logger
// if the middleware is not installed (as in a narrowly-scoped unit test).
func LoggerOf(c *gin.Context) *slog.Logger {
	v, ok := c.Get(loggerKey)
	if !ok {
		return slog.Default()
	}
	log, ok := v.(*slog.Logger)
	if !ok {
		return slog.Default()
	}
	return log
}

// Recovery converts a panic into a 500 with the correlation id attached, logging
// the stack once. Gin's own recovery writes an unstructured trace to stderr and
// an empty body, which tells the client nothing they can report back.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		log.Error("panic recovered",
			slog.String("request_id", RequestIDOf(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Any("panic", recovered),
		)
		httpx.Error(c, http.StatusInternalServerError, httpx.CodeInternal,
			"an unexpected error occurred")
	})
}
