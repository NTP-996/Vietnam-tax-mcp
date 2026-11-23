// Package middleware is the server's Gin chain: request ids, the access log,
// panic recovery, HTTP Basic for /internal/* and the per-IP rate limit
// (ratelimit.go). The access log records the route and status only, never a
// request body or a client address.
package middleware

import (
	"crypto/subtle"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/httpx"
)

// RequestIDHeader is honored inbound and echoed outbound.
const RequestIDHeader = "X-Request-Id"

// maxLoggedPath bounds the raw URL in the access log.
const maxLoggedPath = 256

// RequestID attaches a request id to the context and the response.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(RequestIDHeader)
		if rid == "" || len(rid) > 128 {
			rid = uuid.NewString()
		}
		c.Set("request_id", rid)
		c.Header(RequestIDHeader, rid)
		c.Next()
	}
}

// Observe emits one access-log line per request. The registry argument is
// kept for signature compatibility and must be nil: this build has no
// performance registry.
func Observe(_ any) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		d := time.Since(start)
		route := c.FullPath()
		if route == "" {
			route = "no_match"
		}
		path := c.Request.URL.Path
		if len(path) > maxLoggedPath {
			path = path[:maxLoggedPath]
		}
		status := c.Writer.Status()
		size := c.Writer.Size()
		if size < 0 {
			size = 0
		}
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		slog.Log(c.Request.Context(), level, "request",
			"method", c.Request.Method, "route", route, "path", path,
			"status", status, "bytes", size,
			"dur_ms", float64(d)/float64(time.Millisecond),
			"request_id", c.GetString("request_id"))
	}
}

// Recovery converts panics into the 500 envelope.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, err any) {
		slog.Error("panic", "err", err, "path", c.Request.URL.Path, "request_id", c.GetString("request_id"))
		httpx.Internal(c)
	})
}

// BasicAuth gates /internal/* behind HTTP Basic credentials, compared in
// constant time.
func BasicAuth(username, password string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, p, ok := c.Request.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1
		if !ok || !userOK || !passOK {
			c.Header("WWW-Authenticate", `Basic realm="internal"`)
			httpx.Unauthorized(c, httpx.CodeUnauthorized, "authentication required")
			return
		}
		c.Next()
	}
}
