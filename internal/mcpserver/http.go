package mcpserver

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/httpx"
	"github.com/NTP-996/Vietnam-tax-mcp/internal/middleware"
)

// MaxBodyBytes bounds one request (DESIGN.md §6 payload_too_large).
const MaxBodyBytes = 256 << 10

// HTTPOptions configure the HTTP layer.
type HTTPOptions struct {
	Release        bool
	RatePerMin     int
	MaxInflight    int
	AllowedOrigins []string
	TrustedProxies []string
	StatsUser      string
	StatsPassword  string // empty = /internal/stats is not mounted
}

// Handler builds the gin engine: /mcp (Streamable HTTP, stateless, JSON
// responses), /healthz, the notice page and /internal/stats. Chain (DESIGN.md
// §8): RequestID → Observe (access log, no perf registry) → Recovery →
// origin check → rate limit → in-flight cap → body limit → handler.
func (s *Service) Handler(o HTTPOptions) (http.Handler, error) {
	if o.Release {
		gin.SetMode(gin.ReleaseMode)
	}
	notice, err := s.Notice()
	if err != nil {
		return nil, err
	}
	engine := gin.New()
	if len(o.TrustedProxies) > 0 {
		if err := engine.SetTrustedProxies(o.TrustedProxies); err != nil {
			slog.Warn("invalid TRUSTED_PROXIES; ClientIP() will not trust any proxy", "err", err)
			_ = engine.SetTrustedProxies(nil)
		}
	} else {
		_ = engine.SetTrustedProxies(nil)
	}
	engine.Use(middleware.RequestID(), middleware.Observe(nil), middleware.Recovery(), originCheck(o.AllowedOrigins))

	engine.GET("/", func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'none'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "text/html; charset=utf-8", notice)
	})
	engine.GET("/healthz", func(c *gin.Context) {
		verified := 0
		packs := s.engine.Packs()
		for _, p := range packs {
			if p.Verified {
				verified++
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"status": "ok", "server_version": s.version, "corpus_version": s.corpus.Version,
			"packs": len(packs), "verified_packs": verified, "articles": len(s.corpus.Articles),
		})
	})
	if o.StatsPassword != "" {
		engine.GET("/internal/stats", middleware.BasicAuth(o.StatsUser, o.StatsPassword), func(c *gin.Context) {
			c.JSON(http.StatusOK, s.stats.Snapshot())
		})
	}

	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	rate := o.RatePerMin
	if rate < 1 {
		rate = 300
	}
	inflight := o.MaxInflight
	if inflight < 1 {
		inflight = 32
	}
	mcpChain := []gin.HandlerFunc{
		middleware.RateLimit(nil, "mcp", rate, time.Minute),
		inflightCap(inflight),
		bodyLimit(MaxBodyBytes),
		func(c *gin.Context) {
			// The tool call reads the request id from the headers (telemetry).
			c.Request.Header.Set(RequestIDHeader, c.GetString("request_id"))
			transport.ServeHTTP(c.Writer, c.Request)
		},
	}
	engine.POST("/mcp", mcpChain...)
	engine.GET("/mcp", mcpChain...)
	engine.DELETE("/mcp", mcpChain...)
	return engine, nil
}

// originCheck refuses a request whose Origin is outside the allow-list (the
// transport's DNS-rebinding guard, DESIGN.md §12); a request without Origin (server
// to server — Anthropic's cloud) passes. "http://localhost:*" allows any port.
func originCheck(allowed []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" || originAllowed(origin, allowed) {
			c.Next()
			return
		}
		httpx.Forbidden(c, "forbidden_origin", "origin not allowed")
	}
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if prefix, ok := strings.CutSuffix(a, ":*"); ok {
			port, found := strings.CutPrefix(origin, prefix+":")
			if found && port != "" && strings.Trim(port, "0123456789") == "" {
				return true
			}
			continue
		}
		if origin == a {
			return true
		}
	}
	return false
}

// inflightCap answers 503 overloaded beyond n concurrent requests.
func inflightCap(n int) gin.HandlerFunc {
	slots := make(chan struct{}, n)
	return func(c *gin.Context) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			c.Next()
		default:
			httpx.Error(c, http.StatusServiceUnavailable, "overloaded", "too many calls in flight; please retry")
		}
	}
}

// bodyLimit answers 413 payload_too_large over limit bytes.
func bodyLimit(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > limit {
			httpx.Error(c, http.StatusRequestEntityTooLarge, "payload_too_large", "request body over 256 KiB")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
