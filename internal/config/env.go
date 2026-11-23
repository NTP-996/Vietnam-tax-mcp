package config

// Environment helpers shared by the API and worker configuration (config.go)
// and the MCP server's (mcp.go). They read only the process environment.

import (
	"bufio"
	"log/slog"
	"os"
	"strings"
)

// InsecurePrefix marks every development default in .env.example; staging and
// production refuse any secret that still carries it.
const InsecurePrefix = "dev-insecure-"

func env2(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	case "":
		return def
	default:
		slog.Warn("unrecognized boolean; using default", "key", key, "value", v, "default", def)
		return def
	}
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// loadDotEnv parses KEY=VALUE lines, ignoring blanks and # comments, and never
// overrides a variable that is already set.
func loadDotEnv(path string) {
	f, err := os.Open(path) //nolint:gosec // the operator names the file (ENV_FILE); it is read, never written
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
