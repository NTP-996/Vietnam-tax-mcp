package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// PostingInput is kiem_tra_but_toan's arguments.
type PostingInput struct {
	Regime          string         `json:"regime"`
	TransactionDate string         `json:"transaction_date"`
	Document        rules.Document `json:"document"`
	DraftLines      []DraftLine    `json:"draft_lines"`
	Illustrative    bool           `json:"illustrative,omitempty"`
}

// DraftLine is one line of the draft posting.
type DraftLine struct {
	AccountCode string `json:"account_code"`
	DebitMinor  int64  `json:"debit_minor,omitempty"`
	CreditMinor int64  `json:"credit_minor,omitempty"`
}

// Difference is one way the draft departs from the rule's treatment.
type Difference struct {
	Kind          string `json:"kind"` // unbalanced | missing | unexpected | amount_mismatch
	AccountCode   string `json:"account_code"`
	Side          string `json:"side"`
	ExpectedMinor int64  `json:"expected_minor"`
	DraftMinor    int64  `json:"draft_minor"`
}

// PostingData is kiem_tra_but_toan's data.
type PostingData struct {
	Matches          bool         `json:"matches"`
	Balanced         bool         `json:"balanced"`
	RuleID           string       `json:"rule_id"`
	RulePackVersion  string       `json:"rule_pack_version"`
	NeedsCounterpart bool         `json:"needs_counterpart"`
	ExpectedLines    []rules.Line `json:"expected_lines"`
	Differences      []Difference `json:"differences"`
}

// InvoiceInput is kiem_tra_hoa_don's arguments.
type InvoiceInput struct {
	IssueDate    string         `json:"issue_date"`
	BuyerKind    string         `json:"buyer_kind,omitempty"`
	Draft        map[string]any `json:"draft"`
	Illustrative bool           `json:"illustrative,omitempty"`
}

// InvoiceData is kiem_tra_hoa_don's data.
type InvoiceData struct {
	Checked int                `json:"checked"`
	Passed  bool               `json:"passed"`
	Missing []rules.FieldIssue `json:"missing"`
	Invalid []rules.FieldIssue `json:"invalid"`
}

func (s *Service) registerCheck() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "kiem_tra_but_toan",
		Title:       "Kiểm tra bút toán nháp",
		Description: "So sánh bút toán nháp (theo chế độ kế toán TT133) với cách hạch toán của bộ quy tắc cho chứng từ đó vào ngày giao dịch; trả về từng dòng chênh lệch. Không ghi sổ, không lưu dữ liệu." + descriptionTail,
		InputSchema: inputSchema("kiem_tra_but_toan"),
		Annotations: annotations("Kiểm tra bút toán nháp"),
	}, s.checkPosting)
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "kiem_tra_hoa_don",
		Title:       "Kiểm tra hóa đơn điện tử nháp",
		Description: "Kiểm tra hóa đơn điện tử nháp có đủ các nội dung bắt buộc theo bộ quy tắc có hiệu lực vào ngày lập; trả về nội dung thiếu hoặc sai định dạng. Không phát hành, không ký, không lưu hóa đơn." + descriptionTail,
		InputSchema: inputSchema("kiem_tra_hoa_don"),
		Annotations: annotations("Kiểm tra hóa đơn điện tử nháp"),
	}, s.checkInvoice)
}

type sideKey struct{ account, side string }

func (s *Service) checkPosting(ctx context.Context, _ *mcp.CallToolRequest, in PostingInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	day, ok := parseDay(in.TransactionDate, time.Time{})
	if !ok || in.TransactionDate == "" {
		return s.finish(ctx, invalid(env, "transaction_date", "Định dạng YYYY-MM-DD."))
	}
	env.AsOf = day.Format(time.DateOnly)
	if len(in.DraftLines) == 0 || len(in.DraftLines) > 50 {
		return s.finish(ctx, invalid(env, "draft_lines", "Từ 1 đến 50 dòng."))
	}
	draft := map[sideKey]int64{}
	var debit, credit int64
	for _, l := range in.DraftLines {
		if l.DebitMinor < 0 || l.CreditMinor < 0 || l.DebitMinor > maxRevenue || l.CreditMinor > maxRevenue || (l.DebitMinor == 0 && l.CreditMinor == 0) {
			return s.finish(ctx, invalid(env, "draft_lines", "Mỗi dòng có debit_minor hoặc credit_minor dương, số nguyên đồng."))
		}
		if l.DebitMinor > 0 {
			draft[sideKey{l.AccountCode, "debit"}] += l.DebitMinor
			debit += l.DebitMinor
		}
		if l.CreditMinor > 0 {
			draft[sideKey{l.AccountCode, "credit"}] += l.CreditMinor
			credit += l.CreditMinor
		}
	}
	resp, err := s.engine.Evaluate(rules.Request{Regime: in.Regime, TransactionDate: in.TransactionDate, Document: in.Document})
	switch {
	case errors.Is(err, rules.ErrNoRule):
		return s.finish(ctx, noMatch(env))
	case rules.IsInvalid(err):
		return s.finish(ctx, invalid(env, "document", strings.TrimSuffix(err.Error(), ": invalid request")))
	case err != nil:
		env.Code, env.Message = CodeInternal, msgInternal
		return s.finish(ctx, env)
	}
	pack := s.pack(resp.RulePackVersion)
	if pack == nil {
		env.Code, env.Message = CodeInternal, msgInternal
		return s.finish(ctx, env)
	}
	env.usePack(pack)
	for _, r := range pack.Rules {
		if r.ID == resp.RuleID {
			env.addCitation(s.rowCitation(pack, r.ID, r.Citation, r.Corpus))
		}
	}
	if gate(env, in.Illustrative) {
		return s.finish(ctx, env)
	}
	expected := map[sideKey]int64{}
	for _, l := range resp.Lines {
		if l.DebitMinor > 0 {
			expected[sideKey{l.AccountCode, "debit"}] += l.DebitMinor
		}
		if l.CreditMinor > 0 {
			expected[sideKey{l.AccountCode, "credit"}] += l.CreditMinor
		}
	}
	diffs := []Difference{}
	if debit != credit {
		diffs = append(diffs, Difference{Kind: "unbalanced", Side: "debit-credit", ExpectedMinor: debit, DraftMinor: credit})
	}
	for k, want := range expected {
		got, ok := draft[k]
		switch {
		case !ok:
			diffs = append(diffs, Difference{Kind: "missing", AccountCode: k.account, Side: k.side, ExpectedMinor: want})
		case got != want:
			diffs = append(diffs, Difference{Kind: "amount_mismatch", AccountCode: k.account, Side: k.side, ExpectedMinor: want, DraftMinor: got})
		}
	}
	for k, got := range draft {
		if _, ok := expected[k]; !ok {
			diffs = append(diffs, Difference{Kind: "unexpected", AccountCode: k.account, Side: k.side, DraftMinor: got})
		}
	}
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].Kind != diffs[j].Kind {
			return diffs[i].Kind < diffs[j].Kind
		}
		if diffs[i].AccountCode != diffs[j].AccountCode {
			return diffs[i].AccountCode < diffs[j].AccountCode
		}
		return diffs[i].Side < diffs[j].Side
	})
	data := PostingData{
		Matches: len(diffs) == 0, Balanced: debit == credit, RuleID: resp.RuleID, RulePackVersion: resp.RulePackVersion,
		NeedsCounterpart: resp.NeedsCounterpart, ExpectedLines: resp.Lines, Differences: diffs,
	}
	env.Code, env.Data = CodeOK, data
	if data.Matches {
		env.Message = "Bút toán nháp khớp với cách hạch toán của bộ quy tắc."
	} else {
		env.Message = fmt.Sprintf("Bút toán nháp có %d điểm khác với cách hạch toán của bộ quy tắc.", len(diffs))
	}
	return s.finish(ctx, env)
}

// pack finds a loaded pack by version.
func (s *Service) pack(version string) *rules.Pack {
	packs := s.engine.Packs()
	for i := range packs {
		if packs[i].Version == version {
			return &packs[i]
		}
	}
	return nil
}

func (s *Service) checkInvoice(ctx context.Context, _ *mcp.CallToolRequest, in InvoiceInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	day, ok := parseDay(in.IssueDate, time.Time{})
	if !ok || in.IssueDate == "" {
		return s.finish(ctx, invalid(env, "issue_date", "Định dạng YYYY-MM-DD."))
	}
	env.AsOf = day.Format(time.DateOnly)
	if in.BuyerKind != "" && in.BuyerKind != "business" && in.BuyerKind != "individual" {
		return s.finish(ctx, invalid(env, "buyer_kind", "Một trong: business, individual."))
	}
	if in.Draft == nil {
		return s.finish(ctx, invalid(env, "draft", "Cần một đối tượng hóa đơn nháp."))
	}
	res, err := s.engine.CheckInvoice(day, in.Draft, map[string]string{"buyer_kind": in.BuyerKind})
	if err != nil {
		return s.finish(ctx, noMatch(env))
	}
	for _, p := range res.Packs {
		env.usePack(p)
	}
	for _, r := range res.Rows {
		env.addCitation(s.rowCitation(res.Packs[0], r.ID, r.Citation, r.Corpus))
	}
	if gate(env, in.Illustrative) {
		return s.finish(ctx, env)
	}
	data := InvoiceData{Checked: len(res.Rows), Passed: len(res.Missing) == 0 && len(res.Invalid) == 0, Missing: res.Missing, Invalid: res.Invalid}
	env.Code, env.Data = CodeOK, data
	if data.Passed {
		env.Message = fmt.Sprintf("Hóa đơn nháp có đủ %d nội dung bắt buộc được kiểm tra.", data.Checked)
	} else {
		env.Message = fmt.Sprintf("Hóa đơn nháp thiếu %d và sai %d nội dung bắt buộc.", len(data.Missing), len(data.Invalid))
	}
	if in.BuyerKind == "" {
		env.Message += " Chưa nêu buyer_kind nên mã số thuế người mua không được kiểm tra."
	}
	return s.finish(ctx, env)
}
