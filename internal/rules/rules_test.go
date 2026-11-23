package rules_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// repositoryRules is the repository's rules/ directory relative to this
// package, used when RULES_DIR is not set (go test from services/core).
func rulesDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("RULES_DIR"); dir != "" {
		return dir
	}
	dir := filepath.Join("..", "..", "..", "..", "rules")
	if _, err := os.Stat(filepath.Join(dir, "packs")); err != nil {
		t.Skipf("rules directory not found (%v); set RULES_DIR", err)
	}
	return dir
}

// TestRegression is `make test`: every expected-result table under
// rules/tests against the packs under rules/packs.
func TestRegression(t *testing.T) {
	dir := rulesDir(t)
	engine, err := rules.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	suites, err := rules.LoadSuites(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := 0
	for _, suite := range suites {
		cases += suite.Len()
		for _, failure := range engine.Run(suite) {
			t.Error(failure)
		}
	}
	if cases < 30 {
		t.Errorf("only %d regression cases; the tables have more", cases)
	}
	t.Logf("%d packs, %d suites, %d cases", len(engine.Packs()), len(suites), cases)
}

func TestPacksCarryChecksumAndCitation(t *testing.T) {
	engine, err := rules.Load(rulesDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range engine.Packs() {
		if len(p.Checksum) != 64 {
			t.Errorf("%s: checksum %q is not sha256 hex", p.Path, p.Checksum)
		}
		for _, r := range p.Rules {
			if r.Citation == "" {
				t.Errorf("%s/%s: no citation", p.Version, r.ID)
			}
		}
	}
}

func TestSelectionByDateAndValidation(t *testing.T) {
	jan := rules.Pack{LegalDoc: "X", Article: "Điều 1", Version: "x@2026-01", Regime: "TT133", Path: "a",
		Rules: []rules.Rule{{ID: "r", Citation: "c", When: rules.Match{Document: "invoice"},
			Lines: []rules.LineTemplate{{Account: "131", Side: "debit", Amount: "total"}, {Account: "511", Side: "credit", Amount: "total"}}}}}
	mustDate := func(s string) string { return s }
	_ = mustDate
	// Build dates through the public request path: packs are usually read from
	// YAML, so set the effective range by parsing a tiny document.
	jul := jan
	jul.Version, jul.Path = "x@2026-07", "b"
	setRange(t, &jan, "2026-01-01", "2026-06-30")
	setRange(t, &jul, "2026-07-01", "")

	engine, err := rules.New([]rules.Pack{jan, jul})
	if err != nil {
		t.Fatal(err)
	}
	doc := rules.Document{Kind: "invoice", Direction: "output", TotalMinor: 10}
	for date, want := range map[string]string{"2026-06-30": "x@2026-01", "2026-07-01": "x@2026-07", "2027-01-01": "x@2026-07"} {
		resp, err := engine.Evaluate(rules.Request{Regime: "tt133", TransactionDate: date, Document: doc})
		if err != nil {
			t.Fatalf("%s: %v", date, err)
		}
		if resp.RulePackVersion != want {
			t.Errorf("%s: pack %s, want %s", date, resp.RulePackVersion, want)
		}
	}
	if _, err := engine.Evaluate(rules.Request{Regime: "TT133", TransactionDate: "2025-12-31", Document: doc}); !errors.Is(err, rules.ErrNoRule) {
		t.Errorf("before every pack: %v, want rule_not_found", err)
	}
	if _, err := engine.Evaluate(rules.Request{Regime: "TT133", TransactionDate: "31/12/2025", Document: doc}); !rules.IsInvalid(err) {
		t.Errorf("bad date: %v, want invalid", err)
	}

	// A duplicate version is refused.
	dup := jul
	dup.Path = "c"
	if _, err := rules.New([]rules.Pack{jul, dup}); err == nil {
		t.Error("duplicate pack version accepted")
	}
	// A rule with one line is refused.
	bad := jul
	bad.Rules = []rules.Rule{{ID: "one", Citation: "c", Lines: []rules.LineTemplate{{Account: "131", Side: "debit", Amount: "total"}}}}
	if _, err := rules.New([]rules.Pack{bad}); err == nil {
		t.Error("one-line rule accepted")
	}
}

// setRange parses the effective dates through the pack's own YAML path.
func setRange(t *testing.T, p *rules.Pack, from, to string) {
	t.Helper()
	dir := t.TempDir()
	doc := "legal_doc: X\narticle: a\nversion: v\nregime: TT133\neffective_from: " + from + "\n"
	if to != "" {
		doc += "effective_to: " + to + "\n"
	}
	doc += "rules:\n  - id: r\n    citation: c\n    lines:\n      - {account: '1', side: debit, amount: total}\n      - {account: '2', side: credit, amount: total}\n"
	path := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := rules.ReadPack(path)
	if err != nil {
		t.Fatal(err)
	}
	p.EffectiveFrom, p.EffectiveTo = parsed.EffectiveFrom, parsed.EffectiveTo
}
