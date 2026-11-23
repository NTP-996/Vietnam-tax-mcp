package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MCP is the configuration of cmd/mcp, the public, stateless tax-law MCP
// server. It shares the .env loader and helpers with the API but
// has its own keys: the server has no database, no token and no cache.
type MCP struct {
	Env  string
	Host string
	Port string

	LegalDir string // empty = search (mcpserver.FindLegalDir)
	RulesDir string // empty = search (rules.FindDir)

	PublicURL      string
	ProductName    string
	RatePerMin     int
	MaxInflight    int
	AllowedOrigins []string
	AllowFixtures  bool
	TrustedProxies []string

	StatsUser     string
	StatsPassword string // empty = /internal/stats is not mounted
}

// DefaultProductName is neutral until the product-name check clears.
const DefaultProductName = "Tra cứu luật thuế"

// LoadMCP reads the MCP server's configuration and applies its production
// guard: no fixture packs, a public URL, and no DATABASE_URL — the server
// must not be able to reach a database.
func LoadMCP() (*MCP, error) {
	if path := os.Getenv("ENV_FILE"); path != "" {
		loadDotEnv(path)
	} else {
		loadDotEnv(".env")
		loadDotEnv(filepath.Join("..", "..", ".env"))
	}
	env := strings.ToLower(env2("APP_ENV", "development"))
	switch env {
	case "development", "staging", "production":
	default:
		return nil, fmt.Errorf("APP_ENV %q is not development, staging or production", env)
	}
	origins := env2("MCP_ALLOWED_ORIGINS", "https://claude.ai,https://claude.com")
	if env != "production" {
		origins += ",http://localhost:*,http://127.0.0.1:*"
	}
	c := &MCP{
		Env:            env,
		Host:           env2("HOST", "0.0.0.0"),
		Port:           env2("MCP_PORT", "8090"),
		LegalDir:       os.Getenv("LEGAL_DIR"),
		RulesDir:       os.Getenv("RULES_DIR"),
		PublicURL:      strings.TrimRight(os.Getenv("MCP_PUBLIC_URL"), "/"),
		ProductName:    env2("MCP_PRODUCT_NAME", DefaultProductName),
		RatePerMin:     intEnv("MCP_RATE_PER_MIN", 300),
		MaxInflight:    intEnv("MCP_MAX_INFLIGHT", 32),
		AllowedOrigins: splitCSV(origins),
		AllowFixtures:  boolEnv("MCP_ALLOW_FIXTURE_PACKS", env != "production"),
		TrustedProxies: splitCSV(os.Getenv("TRUSTED_PROXIES")),
		StatsUser:      env2("STATS_USER", "pm"),
		StatsPassword:  os.Getenv("STATS_PASSWORD"),
	}
	if _, err := strconv.Atoi(c.Port); err != nil {
		return nil, fmt.Errorf("MCP_PORT %q is not a port number", c.Port)
	}
	if env != "production" {
		return c, nil
	}
	return c, c.guard()
}

func (c *MCP) guard() error {
	switch {
	case c.AllowFixtures:
		return errors.New("MCP_ALLOW_FIXTURE_PACKS must be false in production")
	case c.PublicURL == "" || !strings.HasPrefix(c.PublicURL, "https://"):
		return errors.New("MCP_PUBLIC_URL must be the https:// address in production")
	case os.Getenv("DATABASE_URL") != "":
		return errors.New("DATABASE_URL is set: the MCP server must not be able to reach a database")
	case strings.Contains(c.StatsPassword, InsecurePrefix):
		return fmt.Errorf("STATS_PASSWORD still carries a %q development default", InsecurePrefix)
	}
	return nil
}

// Addr is the listen address.
func (c *MCP) Addr() string { return c.Host + ":" + c.Port }

// IsProd reports production.
func (c *MCP) IsProd() bool { return c.Env == "production" }

// intEnv parses a positive integer, falling back to the default.
func intEnv(key string, def int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n < 1 {
		return def
	}
	return n
}
