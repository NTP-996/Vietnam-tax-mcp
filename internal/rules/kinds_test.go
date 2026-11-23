package rules_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// legalDir is the repository's legal/ directory (LEGAL_DIR wins), or "" when
// the corpus is not present.
func legalDir() string {
	if dir := os.Getenv("LEGAL_DIR"); dir != "" {
		return dir
	}
	dir := filepath.Join("..", "..", "..", "..", "legal")
	if _, err := os.Stat(filepath.Join(dir, "corpus")); err != nil {
		return ""
	}
	return dir
}

// TestPackCorpusReferences: every row's corpus reference names an article of
// the corpus, checked whenever the corpus is present.
func TestPackCorpusReferences(t *testing.T) {
	dir := legalDir()
	if dir == "" {
		t.Skip("legal corpus not found; set LEGAL_DIR")
	}
	engine, err := rules.Load(rulesDir(t))
	if err != nil {
		t.Fatal(err)
	}
	err = engine.CheckCorpus(func(ref string) bool {
		_, err := os.Stat(filepath.Join(dir, "corpus", ref+".md"))
		return err == nil
	})
	if err != nil {
		t.Error(err)
	}
}

func writePack(t *testing.T, body string) (rules.Pack, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return rules.ReadPack(path)
}

const rateHead = "kind: rate\nlegal_doc: 'Luật X'\narticle: 'Điều 1'\nversion: %s\nregime: HKD\neffective_from: %s\n"

func ratePack(t *testing.T, version, from, rows string) rules.Pack {
	t.Helper()
	p, err := writePack(t, strings.Replace(strings.Replace(rateHead, "%s", version, 1), "%s", from, 1)+rows)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const oneRate = `rates:
  - {id: a, citation: 'c', corpus: 'x-1/dieu-1', tax: gtgt, method: ty_le_doanh_thu, activity_group: g, activity_label: 'G', rate_bp: 100}
`

func TestPackKindValidation(t *testing.T) {
	cases := map[string]string{
		"missing citation":          "rates:\n  - {id: a, corpus: 'x-1/dieu-1', tax: gtgt, method: ty_le_doanh_thu, activity_group: g, activity_label: 'G', rate_bp: 1}\n",
		"verified without signer":   "verified: true\n" + oneRate,
		"foreign list":              oneRate + "fields:\n  - {id: f, citation: c, corpus: 'x-1/dieu-1', path: a, label: A, required: always, check: present}\n",
		"bad corpus reference":      "rates:\n  - {id: a, citation: c, corpus: 'dieu-1', tax: gtgt, method: ty_le_doanh_thu, activity_group: g, activity_label: 'G', rate_bp: 1}\n",
		"rate above 100%":           "rates:\n  - {id: a, citation: c, corpus: 'x-1/dieu-1', tax: gtgt, method: ty_le_doanh_thu, activity_group: g, activity_label: 'G', rate_bp: 10001}\n",
		"khau_tru with a rate":      "rates:\n  - {id: a, citation: c, corpus: 'x-1/dieu-1', tax: gtgt, method: khau_tru, rate_bp: 1}\n",
		"duplicate key in one pack": oneRate + "  - {id: b, citation: 'c', corpus: 'x-1/dieu-1', tax: gtgt, method: ty_le_doanh_thu, activity_group: g, activity_label: 'G', rate_bp: 200}\n",
		"no rows":                   "rates: []\n",
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := writePack(t, strings.Replace(strings.Replace(rateHead, "%s", "v@1", 1), "%s", "2026-01-01", 1)+rows)
			if err != nil {
				return // refused by the strict decoder: fine
			}
			if _, err := rules.New([]rules.Pack{p}); err == nil {
				t.Errorf("%s accepted", name)
			}
		})
	}
	if _, err := writePack(t, "kind: rate\nlegal_doc: X\narticle: a\nversion: v\nregime: R\neffective_from: 2026-01-01\nunknown: 1\n"+oneRate); err == nil {
		t.Error("unknown key accepted")
	}
	verified := ratePack(t, "v@2", "2026-01-01", "verified: true\nverified_by: 'Kế toán trưởng — 2026-12-01'\n"+oneRate)
	if _, err := rules.New([]rules.Pack{verified}); err != nil {
		t.Errorf("signed pack refused: %v", err)
	}
}

func TestOverlappingPacksNeedDisjointKeys(t *testing.T) {
	a := ratePack(t, "a@1", "2026-01-01", oneRate)
	b := ratePack(t, "b@1", "2026-06-01", oneRate)
	if _, err := rules.New([]rules.Pack{a, b}); err == nil {
		t.Error("overlapping packs with the same key accepted")
	}
	c := ratePack(t, "c@1", "2026-06-01", strings.Replace(oneRate, "activity_group: g", "activity_group: h", 1))
	if _, err := rules.New([]rules.Pack{a, c}); err != nil {
		t.Errorf("disjoint keys refused: %v", err)
	}
}

func TestEvaluateIgnoresOtherKinds(t *testing.T) {
	p := ratePack(t, "r@1", "2026-01-01", oneRate)
	p.Regime = "TT133"
	engine, err := rules.New([]rules.Pack{p})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Evaluate(rules.Request{Regime: "TT133", TransactionDate: "2026-07-01", Document: rules.Document{Kind: "invoice"}})
	if !errors.Is(err, rules.ErrNoRule) {
		t.Errorf("Evaluate over a rate pack: %v, want rule_not_found", err)
	}
	if got := engine.Packs()[0].Kind; got != rules.PackKindRate {
		t.Errorf("kind = %q", got)
	}
}

func TestApplyRateRoundsHalfUp(t *testing.T) {
	for _, c := range []struct{ amount, bp, want int64 }{
		{50, 100, 1}, {49, 100, 0}, {150, 100, 2}, {1_000_000_000_000_000, 10000, 1_000_000_000_000_000}, {0, 500, 0},
	} {
		if got := rules.ApplyRate(c.amount, c.bp); got != c.want {
			t.Errorf("ApplyRate(%d, %d) = %d, want %d", c.amount, c.bp, got, c.want)
		}
	}
}

func TestDeadlineListedHoliday(t *testing.T) {
	p, err := writePack(t, `kind: deadline
legal_doc: 'X'
article: 'Điều 1'
version: d@1
regime: QLT
effective_from: 2026-07-01
deadlines:
  - {id: q, citation: c, corpus: 'x-1/dieu-1', tax: gtgt, entity_types: [dn], period: month, due: {rule: day_of_month, months_after_period: 1, day: 20}}
holiday_shift:
  citation: c
  corpus: 'x-1/dieu-2'
  rule: next_working_day
  holidays:
    - {date: 2026-08-20, name: 'Ngày nghỉ thử'}
`)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rules.New([]rules.Pack{p})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Deadline(rules.DeadlineQuery{Tax: "gtgt", EntityType: "dn", Period: rules.Period{Kind: "month", Year: 2026, Number: 7}})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	if !res.Due.Equal(want) || !res.Shifted || res.Holiday == nil || res.Holiday.Name != "Ngày nghỉ thử" {
		t.Errorf("due %s shifted %v holiday %+v", res.Due.Format(rules.DateLayout), res.Shifted, res.Holiday)
	}
}
