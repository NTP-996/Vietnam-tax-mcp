// Command mcp is the public, stateless tax-law MCP server:
// the legal corpus and the rule packs baked into the image, served over
// Streamable HTTP at /mcp. It opens no database and holds no credential
// besides the stats password. `mcp -healthcheck` probes /healthz and exits 0
// or 1 (the distroless image has no shell).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // Asia/Ho_Chi_Minh in distroless

	"github.com/NTP-996/Vietnam-tax-mcp/internal/config"
	"github.com/NTP-996/Vietnam-tax-mcp/internal/mcpserver"
	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// version is the image tag, set with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck(os.Getenv("MCP_PORT")))
	}
	cfg, err := config.LoadMCP()
	if err != nil {
		fail("config", err)
	}
	svc, err := load(cfg)
	if err != nil {
		fail("load", err)
	}
	handler, err := svc.Handler(mcpserver.HTTPOptions{
		Release: cfg.Env != "development", RatePerMin: cfg.RatePerMin, MaxInflight: cfg.MaxInflight,
		AllowedOrigins: cfg.AllowedOrigins, TrustedProxies: cfg.TrustedProxies,
		StatsUser: cfg.StatsUser, StatsPassword: cfg.StatsPassword,
	})
	if err != nil {
		fail("handler", err)
	}
	server := &http.Server{
		Addr: cfg.Addr(), Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		slog.Info("tax-law mcp listening", "addr", cfg.Addr(), "env", cfg.Env, "version", version,
			"corpus_version", svc.Corpus().Version, "articles", len(svc.Corpus().Articles), "packs", len(svc.Engine().Packs()))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fail("server", err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
}

// load reads the packs and the corpus; fixture packs load only when allowed,
// and every pack's corpus reference must name an article.
func load(cfg *config.MCP) (*mcpserver.Service, error) {
	rulesDir, err := rules.FindDir(cfg.RulesDir)
	if err != nil {
		return nil, err
	}
	all, err := rules.Load(rulesDir)
	if err != nil {
		return nil, err
	}
	var packs []rules.Pack
	for _, p := range all.Packs() {
		if p.IsFixture() && !cfg.AllowFixtures {
			continue
		}
		packs = append(packs, p)
	}
	engine, err := rules.New(packs)
	if err != nil {
		return nil, err
	}
	legalDir, err := mcpserver.FindLegalDir(cfg.LegalDir)
	if err != nil {
		return nil, err
	}
	corpus, err := mcpserver.LoadCorpus(legalDir)
	if err != nil {
		return nil, err
	}
	if err := engine.CheckCorpus(corpus.HasArticle); err != nil {
		return nil, err
	}
	slog.Info("corpus and packs loaded", "legal_dir", filepath.Clean(legalDir), "rules_dir", filepath.Clean(rulesDir), "fixtures", cfg.AllowFixtures)
	return mcpserver.New(mcpserver.Options{
		Corpus: corpus, Engine: engine, Version: version, ProductName: cfg.ProductName, PublicURL: cfg.PublicURL,
	}), nil
}

func fail(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}

// healthcheck probes the process's own /healthz; 0 = healthy.
func healthcheck(port string) int {
	if port == "" {
		port = "8090"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil) //nolint:gosec // loopback probe of this process's own numeric port
	if err != nil {
		return 1
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req) //nolint:gosec // see above
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
