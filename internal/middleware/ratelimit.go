package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/httpx"
)

// RateCounter counts hits per key within a fixed window, returning the count
// after this hit and the time until the window resets. Implemented by
// cache.Client (Valkey) so limits hold across API instances.
type RateCounter interface {
	Incr(ctx context.Context, key string, window time.Duration) (count int64, retryAfter time.Duration, err error)
}

// memCounter is the in-process fixed-window counter: the default backend and
// the fallback when the shared counter is unreachable.
type memCounter struct {
	mu sync.Mutex
	m  map[string]*memWindow
}

type memWindow struct {
	count int64
	reset time.Time
}

func newMemCounter() *memCounter { return &memCounter{m: make(map[string]*memWindow)} }

func (mc *memCounter) incr(key string, window time.Duration) (int64, time.Duration) {
	now := time.Now()
	mc.mu.Lock()
	defer mc.mu.Unlock()
	w := mc.m[key]
	if w == nil || now.After(w.reset) {
		w = &memWindow{reset: now.Add(window)}
		mc.m[key] = w
	}
	w.count++
	if len(mc.m) > 10000 { // opportunistic cleanup of expired windows
		for k, v := range mc.m {
			if now.After(v.reset) {
				delete(mc.m, k)
			}
		}
	}
	return w.count, time.Until(w.reset)
}

// RateLimit is a fixed-window limiter keyed by client IP — for the credential
// endpoints, where brute force has no user id to key on. A nil shared counter
// means in-process only; when the shared counter errors it falls back to the
// in-process window, so the limiter never fails open and a cache outage never
// blocks logins.
func RateLimit(shared RateCounter, scope string, limit int, window time.Duration) gin.HandlerFunc {
	mem := newMemCounter()
	return func(c *gin.Context) {
		k := c.ClientIP()
		var (
			count int64
			retry time.Duration
		)
		counted := false
		if shared != nil {
			n, ra, err := shared.Incr(c.Request.Context(), "ratelimit:"+scope+":"+k, window)
			if err == nil {
				count, retry, counted = n, ra, true
			} else {
				slog.Warn("shared rate limiter unavailable; using in-process window", "scope", scope, "err", err)
			}
		}
		if !counted {
			count, retry = mem.incr(k, window)
		}
		if count > int64(limit) {
			c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			httpx.Error(c, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many attempts; please try again later")
			return
		}
		c.Next()
	}
}
