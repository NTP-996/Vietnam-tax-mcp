package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/mcpserver"
	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// repoDir resolves rules/ or legal/ of the repository from this package.
func repoDir(t *testing.T, env, name, probe string) string {
	t.Helper()
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	dir := filepath.Join("..", "..", "..", "..", name)
	if _, err := os.Stat(filepath.Join(dir, probe)); err != nil {
		t.Skipf("%s not found (%v); set %s", name, err, env)
	}
	return dir
}

var (
	loadOnce   sync.Once
	corpus     *mcpserver.Corpus
	engine     *rules.Engine
	errLoading error
)

func load(t *testing.T) (*mcpserver.Corpus, *rules.Engine) {
	t.Helper()
	rulesDir := repoDir(t, "RULES_DIR", "rules", "packs")
	legalDir := repoDir(t, "LEGAL_DIR", "legal", "manifest.yaml")
	loadOnce.Do(func() {
		engine, errLoading = rules.Load(rulesDir)
		if errLoading != nil {
			return
		}
		corpus, errLoading = mcpserver.LoadCorpus(legalDir)
		if errLoading == nil {
			errLoading = engine.CheckCorpus(corpus.HasArticle)
		}
	})
	if errLoading != nil {
		t.Fatal(errLoading)
	}
	return corpus, engine
}

// syncBuffer collects log output from concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fixture struct {
	svc     *mcpserver.Service
	session *mcp.ClientSession
	logs    *syncBuffer
}

// newFixture connects an SDK client to the server over the in-memory
// transport. "Today" is fixed at 2026-11-22.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	c, e := load(t)
	logs := &syncBuffer{}
	svc := mcpserver.New(mcpserver.Options{
		Corpus: c, Engine: e, Version: "mcp-test",
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		Now:    func() time.Time { return time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC) },
	})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := svc.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &fixture{svc: svc, session: session, logs: logs}
}

type envelope struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	Citations []struct {
		ID        string `json:"id"`
		Citation  string `json:"citation"`
		Verified  bool   `json:"verified"`
		SourceURL string `json:"source_url"`
	} `json:"citations"`
	Verified     bool    `json:"verified"`
	Illustrative bool    `json:"illustrative"`
	Label        *string `json:"label"`
	AsOf         string  `json:"as_of"`
	Versions     struct {
		Corpus string   `json:"corpus"`
		Packs  []string `json:"packs"`
		Server string   `json:"server"`
	} `json:"versions"`
	Disclaimer string `json:"disclaimer"`
	isError    bool
}

func (f *fixture) call(t *testing.T, tool string, args any) envelope {
	t.Helper()
	res, err := f.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s: structured content %s: %v", tool, raw, err)
	}
	env.isError = res.IsError
	// The envelope's fields are always present.
	if env.Disclaimer != mcpserver.Disclaimer || env.Versions.Server != "mcp-test" || env.Versions.Corpus == "" || env.AsOf == "" || env.Citations == nil {
		t.Errorf("%s: envelope incomplete: %s", tool, raw)
	}
	return env
}

func TestToolsListEightReadOnly(t *testing.T) {
	f := newFixture(t)
	res, err := f.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 8 {
		t.Fatalf("%d tools, want 8", len(res.Tools))
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || a.Title == "" || tool.Title == "" || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: annotations %+v", tool.Name, a)
		}
		if !strings.HasSuffix(tool.Description, "không dán dữ liệu thật của khách hàng.") || len([]rune(tool.Description)) > 400 {
			t.Errorf("%s: description %q", tool.Name, tool.Description)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s: no input schema", tool.Name)
		}
	}
	init := f.session.InitializeResult()
	if init == nil || !strings.Contains(init.Instructions, "Không bao giờ nêu thuế suất") {
		t.Error("initialize did not carry the instructions")
	}
}

func TestLookupTools(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name, tool string
		args       map[string]any
		code       string
		isError    bool
		check      func(t *testing.T, env envelope)
	}{
		{"search finds the invoice content article", "tra_cuu_van_ban", map[string]any{"query": "noi dung cua hoa don", "doc": "254/2026/NĐ-CP"}, "ok", false,
			func(t *testing.T, env envelope) {
				if !strings.Contains(string(env.Data), "nd-254-2026/dieu-10") {
					t.Errorf("nd-254-2026/dieu-10 not among the hits: %s", env.Data)
				}
				if env.Verified || len(env.Citations) == 0 || env.Citations[0].Verified {
					t.Error("unverified corpus reported as verified")
				}
			}},
		{"search before the cutover excludes NĐ 254", "tra_cuu_van_ban", map[string]any{"query": "hóa đơn điện tử", "doc": "nd-254-2026", "as_of": "2026-06-30"}, "no_match", false, nil},
		{"search with nothing matching", "tra_cuu_van_ban", map[string]any{"query": "zzqqxx wwvvyy"}, "no_match", false, nil},
		{"search with a bad date", "tra_cuu_van_ban", map[string]any{"query": "thuế", "as_of": "2026-02-30"}, "invalid_argument", true, nil},
		{"search with a one-letter query (schema)", "tra_cuu_van_ban", map[string]any{"query": "a"}, "invalid_argument", true, nil},
		{"search with an unknown key (schema)", "tra_cuu_van_ban", map[string]any{"query": "thuế", "foo": 1}, "invalid_argument", true, nil},
		{"read an article verbatim", "doc_dieu_khoan", map[string]any{"doc": "NĐ 254/2026/NĐ-CP", "dieu": 10}, "ok", false,
			func(t *testing.T, env envelope) {
				var d struct{ Text string }
				_ = json.Unmarshal(env.Data, &d)
				if !strings.Contains(d.Text, "Hóa đơn phải có các nội dung sau") {
					t.Errorf("text: %.200s", d.Text)
				}
				if env.Citations[0].Citation != "NĐ 254/2026/NĐ-CP, Điều 10 — effective 2026-07-01" {
					t.Errorf("citation %q", env.Citations[0].Citation)
				}
			}},
		{"read one điểm", "doc_dieu_khoan", map[string]any{"doc": "nd-254-2026", "dieu": 10, "khoan": 1, "diem": "b"}, "ok", false,
			func(t *testing.T, env envelope) {
				var d struct{ Text string }
				_ = json.Unmarshal(env.Data, &d)
				if d.Text != "b) Số hóa đơn;" {
					t.Errorf("điểm b = %q", d.Text)
				}
			}},
		{"read before the cutover", "doc_dieu_khoan", map[string]any{"doc": "nd-254-2026", "dieu": 10, "as_of": "2026-06-30"}, "not_in_force", true,
			func(t *testing.T, env envelope) {
				if strings.Contains(string(env.Data), "Hóa đơn phải có") {
					t.Error("text returned for a date outside the range")
				}
			}},
		{"read a missing article", "doc_dieu_khoan", map[string]any{"doc": "nd-254-2026", "dieu": 999}, "no_match", false, nil},
		{"read a missing document", "doc_dieu_khoan", map[string]any{"doc": "NĐ 999/2030/NĐ-CP", "dieu": 1}, "no_match", false, nil},
		{"điểm without khoản", "doc_dieu_khoan", map[string]any{"doc": "nd-254-2026", "dieu": 10, "diem": "a"}, "invalid_argument", true, nil},
		{"applicable on the cutover day", "van_ban_ap_dung", map[string]any{"topic": "hoa_don", "as_of": "2026-07-01"}, "ok", false,
			func(t *testing.T, env envelope) {
				if !strings.Contains(string(env.Data), "254/2026/NĐ-CP") || !strings.Contains(string(env.Data), "NĐ 123/2020/NĐ-CP") {
					t.Errorf("documents: %s", env.Data)
				}
			}},
		{"applicable the day before the cutover", "van_ban_ap_dung", map[string]any{"topic": "hoa_don", "as_of": "2026-06-30"}, "no_match", false,
			func(t *testing.T, env envelope) {
				var d struct {
					Upcoming []struct{ ID string } `json:"upcoming"`
				}
				_ = json.Unmarshal(env.Data, &d)
				if len(d.Upcoming) == 0 {
					t.Errorf("no upcoming documents: %s", env.Data)
				}
			}},
		{"applicable with an unknown topic (schema)", "van_ban_ap_dung", map[string]any{"topic": "xyz", "as_of": "2026-07-01"}, "invalid_argument", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := f.call(t, c.tool, c.args)
			if env.Code != c.code || env.isError != c.isError {
				t.Fatalf("code %s (isError %v), want %s (%v): %s", env.Code, env.isError, c.code, c.isError, env.Message)
			}
			if c.check != nil {
				c.check(t, env)
			}
		})
	}
}

func TestCalcAndCheckTools(t *testing.T) {
	f := newFixture(t)
	q3 := map[string]any{"kind": "quarter", "year": 2026, "number": 3}
	hkd := map[string]any{"period": q3, "revenue_by_group": []map[string]any{{"activity_group": "phan_phoi", "revenue_minor": 300000000}, {"activity_group": "dich_vu", "revenue_minor": 12345679}}}
	withIll := func(m map[string]any) map[string]any {
		out := map[string]any{"illustrative": true}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	draft := map[string]any{
		"form": "1", "series": "C26TAA", "number": "1", "issued_on": "2026-08-01",
		"seller":    map[string]any{"name": "Công ty Mẫu", "address": "Hà Nội", "mst": "0100000000"},
		"buyer":     map[string]any{"name": "Công ty Ví Dụ", "address": "Huế"},
		"lines":     []any{map[string]any{"name": "Hàng", "unit": "cái", "quantity": "1", "unit_price": "1000", "amount_minor": 1000, "vat_rate": "10"}},
		"vat_rates": []any{map[string]any{"rate": "10"}}, "vat_minor": 100, "total_minor": 1100,
	}
	invoice := map[string]any{"issue_date": "2026-08-01", "buyer_kind": "business", "draft": draft}
	posting := map[string]any{
		"regime": "TT133", "transaction_date": "2026-07-15",
		"document":    map[string]any{"kind": "invoice", "direction": "output", "vat_rate": "10", "subtotal_minor": 1000000, "vat_minor": 100000, "total_minor": 1100000},
		"draft_lines": []map[string]any{{"account_code": "131", "debit_minor": 1100000}, {"account_code": "511", "credit_minor": 1000000}, {"account_code": "3331", "credit_minor": 90000}},
	}
	cases := []struct {
		name, tool string
		args       map[string]any
		code       string
		check      func(t *testing.T, env envelope)
	}{
		{"hkd refused: unverified pack", "tinh_thue_hkd", hkd, "unverified_pack", func(t *testing.T, env envelope) {
			if string(env.Data) != "null" || env.Illustrative {
				t.Errorf("a figure leaked: %s", env.Data)
			}
		}},
		{"hkd illustrative", "tinh_thue_hkd", withIll(hkd), "ok", func(t *testing.T, env envelope) {
			var d struct {
				Totals      struct{ GTGTMinor, TNCNMinor *int64 } `json:"totals"`
				Unavailable []string                              `json:"unavailable"`
			}
			raw := strings.NewReplacer(`"gtgt_minor"`, `"GTGTMinor"`, `"tncn_minor"`, `"TNCNMinor"`).Replace(string(env.Data))
			_ = json.Unmarshal([]byte(raw), &d)
			// 300,000,000 × 1% + 12,345,679 × 5% (617,283.95 → 617,284)
			if d.Totals.GTGTMinor == nil || *d.Totals.GTGTMinor != 3617284 || d.Totals.TNCNMinor != nil {
				t.Errorf("totals %s", env.Data)
			}
			if len(d.Unavailable) != 1 || d.Unavailable[0] != "tncn" {
				t.Errorf("unavailable %v", d.Unavailable)
			}
			// A quarter cannot test the annual threshold: the message must say so.
			if !strings.Contains(env.Message, "Ngưỡng doanh thu được xét theo năm") {
				t.Errorf("message %q does not warn about the annual threshold", env.Message)
			}
			if !env.Illustrative || env.Label == nil || *env.Label != mcpserver.IllustrativeLabel || env.Verified {
				t.Error("illustrative answer not labeled")
			}
		}},
		{"hkd before the pack (khoán era)", "tinh_thue_hkd", withIll(map[string]any{"period": map[string]any{"kind": "quarter", "year": 2025, "number": 4}, "revenue_by_group": hkd["revenue_by_group"]}), "no_match", nil},
		{"hkd unknown group lists the groups", "tinh_thue_hkd", withIll(map[string]any{"period": q3, "revenue_by_group": []map[string]any{{"activity_group": "vang", "revenue_minor": 1}}}), "invalid_argument", func(t *testing.T, env envelope) {
			if !strings.Contains(env.Message, "phan_phoi") {
				t.Errorf("message %q does not list the groups", env.Message)
			}
		}},
		{"hkd year under the threshold", "tinh_thue_hkd", withIll(map[string]any{"period": map[string]any{"kind": "year", "year": 2026}, "revenue_by_group": []map[string]any{{"activity_group": "khac", "revenue_minor": 900000000}}}), "ok", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), `"applies":true`) || !strings.Contains(string(env.Data), `"gtgt_minor":0`) {
				t.Errorf("threshold not applied: %s", env.Data)
			}
		}},
		{"hkd negative revenue (schema)", "tinh_thue_hkd", withIll(map[string]any{"period": q3, "revenue_by_group": []map[string]any{{"activity_group": "khac", "revenue_minor": -1}}}), "invalid_argument", nil},
		{"hkd bad quarter", "tinh_thue_hkd", withIll(map[string]any{"period": map[string]any{"kind": "quarter", "year": 2026, "number": 5}, "revenue_by_group": hkd["revenue_by_group"]}), "invalid_argument", nil},
		{"gtgt deduction with carry-forward", "tinh_thue_gtgt", map[string]any{"method": "khau_tru", "period": q3, "illustrative": true, "khau_tru": map[string]any{"output_vat_minor": 100, "input_vat_deductible_minor": 150, "carried_forward_minor": 20}}, "ok", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), `"payable_minor":0`) || !strings.Contains(string(env.Data), `"carry_forward_minor":70`) {
				t.Errorf("data %s", env.Data)
			}
		}},
		{"gtgt deduction refused unverified", "tinh_thue_gtgt", map[string]any{"method": "khau_tru", "period": q3, "khau_tru": map[string]any{"output_vat_minor": 100, "input_vat_deductible_minor": 50}}, "unverified_pack", nil},
		{"gtgt direct", "tinh_thue_gtgt", map[string]any{"method": "truc_tiep", "period": q3, "illustrative": true, "truc_tiep": map[string]any{"revenue_by_group": []map[string]any{{"activity_group": "san_xuat_van_tai", "revenue_minor": 1000000}}}}, "ok", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), `"payable_minor":30000`) {
				t.Errorf("data %s", env.Data)
			}
		}},
		{"gtgt wrong method object", "tinh_thue_gtgt", map[string]any{"method": "khau_tru", "period": q3, "truc_tiep": map[string]any{"revenue_by_group": []map[string]any{{"activity_group": "khac", "revenue_minor": 1}}}}, "invalid_argument", nil},
		{"gtgt period straddling Luật 48", "tinh_thue_gtgt", map[string]any{"method": "khau_tru", "period": map[string]any{"kind": "year", "year": 2025}, "illustrative": true, "khau_tru": map[string]any{"output_vat_minor": 1, "input_vat_deductible_minor": 1}}, "period_spans_versions", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), "2025-07-01") {
				t.Errorf("boundary %s", env.Data)
			}
		}},
		{"deadline shifted off a Saturday", "han_nop", map[string]any{"entity_type": "dn", "tax": "gtgt", "period": q3, "illustrative": true}, "ok", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), `"due_date":"2026-11-02"`) || !strings.Contains(string(env.Data), `"shifted":true`) {
				t.Errorf("data %s", env.Data)
			}
		}},
		{"deadline refused unverified", "han_nop", map[string]any{"entity_type": "hkd", "tax": "gtgt", "period": q3}, "unverified_pack", nil},
		{"deadline before the cutover", "han_nop", map[string]any{"entity_type": "dn", "tax": "gtgt", "period": map[string]any{"kind": "quarter", "year": 2026, "number": 2}, "illustrative": true}, "no_match", nil},
		{"deadline unknown entity (schema)", "han_nop", map[string]any{"entity_type": "x", "tax": "gtgt", "period": q3}, "invalid_argument", nil},
		{"posting check finds the VAT difference", "kiem_tra_but_toan", withIll(posting), "ok", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), `"amount_mismatch"`) || !strings.Contains(string(env.Data), `"unbalanced"`) || !env.Illustrative {
				t.Errorf("data %s", env.Data)
			}
		}},
		{"posting check refused on a fixture pack", "kiem_tra_but_toan", posting, "unverified_pack", nil},
		{"posting check before every pack", "kiem_tra_but_toan", withIll(map[string]any{"regime": "TT133", "transaction_date": "2025-12-31", "document": posting["document"], "draft_lines": posting["draft_lines"]}), "no_match", nil},
		{"posting check bad account (schema)", "kiem_tra_but_toan", withIll(map[string]any{"regime": "TT133", "transaction_date": "2026-07-15", "document": posting["document"], "draft_lines": []map[string]any{{"account_code": "ab"}}}), "invalid_argument", nil},
		{"invoice check finds the missing buyer MST", "kiem_tra_hoa_don", withIll(invoice), "ok", func(t *testing.T, env envelope) {
			if !strings.Contains(string(env.Data), `"mst-nguoi-mua"`) || !strings.Contains(string(env.Data), `"passed":false`) {
				t.Errorf("data %s", env.Data)
			}
		}},
		{"invoice check refused unverified", "kiem_tra_hoa_don", invoice, "unverified_pack", nil},
		{"invoice check before the cutover", "kiem_tra_hoa_don", withIll(map[string]any{"issue_date": "2026-06-30", "draft": draft}), "no_match", nil},
		{"invoice check bad buyer kind (schema)", "kiem_tra_hoa_don", map[string]any{"issue_date": "2026-08-01", "buyer_kind": "robot", "draft": draft}, "invalid_argument", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := f.call(t, c.tool, c.args)
			if env.Code != c.code {
				t.Fatalf("code %s, want %s: %s", env.Code, c.code, env.Message)
			}
			if env.code() != env.isError {
				t.Errorf("isError %v for %s", env.isError, env.Code)
			}
			if c.code == "ok" && (len(env.Citations) == 0 || len(env.Versions.Packs) == 0) {
				t.Errorf("an answer without a citation or pack version")
			}
			if c.check != nil {
				c.check(t, env)
			}
		})
	}
}

func (e envelope) code() bool {
	switch e.Code {
	case "ok", "no_match":
		return false
	}
	return true
}

// TestNoArgumentReachesTheLogs puts a sentinel in every argument of every
// tool; neither the log output nor the stats may contain it.
func TestNoArgumentReachesTheLogs(t *testing.T) {
	f := newFixture(t)
	const sentinel = "SENTINEL7731"
	const num = 7731773177
	period := map[string]any{"kind": "year", "year": 2026}
	calls := map[string]map[string]any{
		"tra_cuu_van_ban":   {"query": "thuế " + sentinel, "doc": sentinel},
		"doc_dieu_khoan":    {"doc": sentinel, "dieu": 1},
		"van_ban_ap_dung":   {"topic": sentinel, "as_of": "2026-07-01"},
		"tinh_thue_hkd":     {"period": period, "illustrative": true, "revenue_by_group": []map[string]any{{"activity_group": sentinel, "revenue_minor": num}}},
		"tinh_thue_gtgt":    {"method": "khau_tru", "period": period, "illustrative": true, "khau_tru": map[string]any{"output_vat_minor": num, "input_vat_deductible_minor": num}},
		"han_nop":           {"entity_type": "dn", "tax": "gtgt", "method": sentinel, "period": period},
		"kiem_tra_but_toan": {"regime": "TT133", "transaction_date": "2026-07-15", "illustrative": true, "document": map[string]any{"kind": "invoice", "direction": "output", "vat_rate": "10", "total_minor": num}, "draft_lines": []map[string]any{{"account_code": "131", "debit_minor": num}}},
		"kiem_tra_hoa_don":  {"issue_date": "2026-08-01", "illustrative": true, "draft": map[string]any{"seller": map[string]any{"name": sentinel, "mst": sentinel}, "note": sentinel}},
	}
	for tool, args := range calls {
		f.call(t, tool, args)
	}
	logs := f.logs.String()
	stats, _ := json.Marshal(f.svc.Stats().Snapshot())
	for _, s := range []string{sentinel, "7731773177", "thuế"} {
		if strings.Contains(logs, s) || strings.Contains(string(stats), s) {
			t.Errorf("%q reached the logs or stats:\n%s\n%s", s, logs, stats)
		}
	}
	if n := strings.Count(logs, `"msg":"tool_call"`); n != len(calls) {
		t.Errorf("%d tool_call lines, want %d:\n%s", n, len(calls), logs)
	}
}

func TestSearchFoldingAndRanking(t *testing.T) {
	c, _ := load(t)
	asOf := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	folded := c.Search("hoa don dien tu", asOf, nil, 5)
	exact := c.Search("hóa đơn điện tử", asOf, nil, 5)
	if len(folded) == 0 || len(exact) == 0 {
		t.Fatal("no hits")
	}
	if exact[0].Score <= folded[0].Score && exact[0].Article == folded[0].Article {
		t.Errorf("exact diacritics did not rank higher: %v vs %v", exact[0].Score, folded[0].Score)
	}
	for _, h := range exact {
		if len([]rune(h.Snippet)) > 242 || h.Snippet == "" {
			t.Errorf("snippet %d runes", len([]rune(h.Snippet)))
		}
	}
	before := c.Search("hóa đơn điện tử", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), nil, 50)
	for _, h := range before {
		if h.Article.Doc.ID == "nd-254-2026" || h.Article.Doc.ID == "tt-91-2026" {
			t.Errorf("%s returned before it is in force", h.Article.ID)
		}
	}
}

func TestHTTPTransport(t *testing.T) {
	c, e := load(t)
	svc := mcpserver.New(mcpserver.Options{Corpus: c, Engine: e, Version: "mcp-test", Logger: slog.New(slog.NewJSONHandler(&syncBuffer{}, nil))})
	h, err := svc.Handler(mcpserver.HTTPOptions{Release: true, RatePerMin: 5, MaxInflight: 4, AllowedOrigins: []string{"https://claude.ai", "http://localhost:*"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	// An SDK client over Streamable HTTP.
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("tools/list over HTTP: %v (%d)", err, len(tools.Tools))
	}
	_ = session.Close()

	post := func(origin string, body []byte) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	ping := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if got := post("https://evil.example", ping); got != http.StatusForbidden {
		t.Errorf("foreign origin: %d, want 403", got)
	}
	if got := post("", bytes.Repeat([]byte("x"), mcpserver.MaxBodyBytes+1)); got != http.StatusRequestEntityTooLarge {
		t.Errorf("big body: %d, want 413", got)
	}
	limited := false
	for range 10 {
		if post("", ping) == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("no 429 after the rate limit")
	}

	resp, err := http.Get(srv.URL + "/healthz") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&health)
	_ = resp.Body.Close()
	for _, k := range []string{"status", "server_version", "corpus_version", "packs", "verified_packs", "articles"} {
		if _, ok := health[k]; !ok {
			t.Errorf("/healthz lacks %s: %v", k, health)
		}
	}
	resp, err = http.Get(srv.URL + "/") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	var page bytes.Buffer
	_, _ = page.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(page.String(), `lang="vi"`) || strings.Contains(page.String(), "<script") || !strings.Contains(page.String(), "Máy chủ không lưu nội dung bạn gửi") {
		t.Error("notice page incomplete")
	}
}
