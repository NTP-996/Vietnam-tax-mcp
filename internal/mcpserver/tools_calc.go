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

// RevenueLine is one activity group's revenue (integer VND).
type RevenueLine struct {
	ActivityGroup string `json:"activity_group"`
	RevenueMinor  int64  `json:"revenue_minor"`
}

// HKDInput is tinh_thue_hkd's arguments.
type HKDInput struct {
	Period         rules.Period  `json:"period"`
	RevenueByGroup []RevenueLine `json:"revenue_by_group"`
	Illustrative   bool          `json:"illustrative,omitempty"`
}

// TaxLine is one activity group's computed tax. A nil rate means no rule in
// force covers that tax (it is listed in Unavailable), never zero.
type TaxLine struct {
	ActivityGroup string   `json:"activity_group"`
	ActivityLabel string   `json:"activity_label"`
	RevenueMinor  int64    `json:"revenue_minor"`
	GTGTRateBP    *int64   `json:"gtgt_rate_bp"`
	GTGTMinor     *int64   `json:"gtgt_minor"`
	TNCNRateBP    *int64   `json:"tncn_rate_bp"`
	TNCNMinor     *int64   `json:"tncn_minor"`
	RowIDs        []string `json:"row_ids"`
}

// HKDData is tinh_thue_hkd's data.
type HKDData struct {
	Lines       []TaxLine     `json:"lines"`
	Totals      HKDTotals     `json:"totals"`
	Threshold   *HKDThreshold `json:"threshold"`
	Unavailable []string      `json:"unavailable"` // taxes with no rule in force (e.g. tncn)
	Rounding    string        `json:"rounding"`
}

// HKDTotals sums the lines.
type HKDTotals struct {
	GTGTMinor *int64 `json:"gtgt_minor"`
	TNCNMinor *int64 `json:"tncn_minor"`
}

// HKDThreshold is the annual revenue threshold; Applies is computed for a
// year only (revenue total ≤ the threshold).
type HKDThreshold struct {
	AmountMinor int64 `json:"amount_minor"`
	Applies     *bool `json:"applies"`
}

// GTGTInput is tinh_thue_gtgt's arguments.
type GTGTInput struct {
	Method   string       `json:"method"`
	Period   rules.Period `json:"period"`
	KhauTru  *KhauTruIn   `json:"khau_tru,omitempty"`
	TrucTiep *struct {
		RevenueByGroup []RevenueLine `json:"revenue_by_group"`
	} `json:"truc_tiep,omitempty"`
	Illustrative bool `json:"illustrative,omitempty"`
}

// KhauTruIn is the deduction method's amounts.
type KhauTruIn struct {
	OutputVATMinor      int64 `json:"output_vat_minor"`
	InputVATDeductible  int64 `json:"input_vat_deductible_minor"`
	CarriedForwardMinor int64 `json:"carried_forward_minor,omitempty"`
}

// GTGTData is tinh_thue_gtgt's data.
type GTGTData struct {
	Method            string    `json:"method"`
	PayableMinor      int64     `json:"payable_minor"`
	CarryForwardMinor int64     `json:"carry_forward_minor"`
	Lines             []TaxLine `json:"lines"`
	Rounding          string    `json:"rounding,omitempty"`
}

// DeadlineInput is han_nop's arguments.
type DeadlineInput struct {
	EntityType   string       `json:"entity_type"`
	Tax          string       `json:"tax"`
	Method       string       `json:"method,omitempty"`
	Period       rules.Period `json:"period"`
	Illustrative bool         `json:"illustrative,omitempty"`
}

// DeadlineData is han_nop's data.
type DeadlineData struct {
	DueDate          string       `json:"due_date"`
	UnshiftedDueDate string       `json:"unshifted_due_date"`
	Shifted          bool         `json:"shifted"`
	Holiday          *HolidayInfo `json:"holiday"`
	RowID            string       `json:"row_id"`
}

// HolidayInfo is the listed holiday a due date stepped over.
type HolidayInfo struct {
	Date string `json:"date"`
	Name string `json:"name"`
}

// roundingNote is cited with every computed amount.
const roundingNote = "Số tiền = doanh thu × tỷ lệ (điểm cơ bản) / 10000, làm tròn nửa lên đến đồng theo từng dòng rồi cộng — quy ước của bộ quy tắc, chờ kế toán trưởng xác nhận."

const maxRevenue = 1_000_000_000_000_000

func (s *Service) registerCalc() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "tinh_thue_hkd",
		Title:       "Tính thuế hộ kinh doanh",
		Description: "Tính thuế GTGT (và TNCN khi có quy tắc) của hộ kinh doanh theo tỷ lệ % trên doanh thu từng nhóm ngành nghề trong một kỳ, từ bộ quy tắc có phiên bản. Bộ quy tắc chưa xác minh chỉ cho kết quả khi illustrative = true." + descriptionTail,
		InputSchema: inputSchema("tinh_thue_hkd"),
		Annotations: annotations("Tính thuế hộ kinh doanh"),
	}, s.calcHKD)
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "tinh_thue_gtgt",
		Title:       "Tính thuế GTGT phải nộp",
		Description: "Tính thuế GTGT phải nộp trong kỳ: phương pháp khấu trừ (thuế đầu ra trừ đầu vào được khấu trừ và số chuyển kỳ trước) hoặc phương pháp trực tiếp theo tỷ lệ % trên doanh thu. Số tiền là đồng, số nguyên." + descriptionTail,
		InputSchema: inputSchema("tinh_thue_gtgt"),
		Annotations: annotations("Tính thuế GTGT phải nộp"),
	}, s.calcGTGT)
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "han_nop",
		Title:       "Tra hạn nộp hồ sơ khai thuế",
		Description: "Tra hạn nộp hồ sơ khai thuế theo loại người nộp (dn, hkd), loại thuế và kỳ (tháng, quý, năm) từ bộ quy tắc, kể cả lùi hạn khi trùng ngày nghỉ. Bộ quy tắc chưa xác minh chỉ cho kết quả khi illustrative = true." + descriptionTail,
		InputSchema: inputSchema("han_nop"),
		Annotations: annotations("Tra hạn nộp hồ sơ khai thuế"),
	}, s.deadline)
}

// checkPeriod validates a period argument.
func checkPeriod(env *Envelope, p rules.Period) bool {
	if err := p.Validate(); err != nil {
		invalid(env, "period", strings.TrimSuffix(err.Error(), ": invalid request"))
		return false
	}
	return true
}

// rateLines computes the revenue lines of one regime and method, selecting
// the packs on the period's last day. It returns the envelope code to stop
// with ("" = go on).
func (s *Service) rateLines(env *Envelope, regime, method string, p rules.Period, revenue []RevenueLine) ([]TaxLine, []string, string) {
	if len(revenue) == 0 || len(revenue) > 10 {
		invalid(env, "revenue_by_group", "Từ 1 đến 10 dòng.")
		return nil, nil, CodeInvalidArgument
	}
	first, last := p.First(), p.Last()
	groups := s.engine.Groups(regime, "gtgt", method, last)
	if len(groups) == 0 {
		noMatch(env)
		return nil, nil, CodeNoMatch
	}
	known := map[string]bool{}
	for _, g := range groups {
		known[g.Row.ActivityGroup] = true
	}
	var lines []TaxLine
	unavailable := map[string]bool{}
	for _, r := range revenue {
		if r.RevenueMinor < 0 || r.RevenueMinor > maxRevenue {
			invalid(env, "revenue_minor", "Số nguyên từ 0 đến 10^15 đồng.")
			return nil, nil, CodeInvalidArgument
		}
		if !known[r.ActivityGroup] {
			invalid(env, "activity_group", "Nhóm ngành nghề có hiệu lực: "+groupList(groups)+".")
			return nil, nil, CodeInvalidArgument
		}
		line := TaxLine{ActivityGroup: r.ActivityGroup, RevenueMinor: r.RevenueMinor, RowIDs: []string{}}
		for _, tax := range []string{"gtgt", "tncn"} {
			m, err := s.engine.Rate(regime, tax, method, r.ActivityGroup, last)
			if errors.Is(err, rules.ErrNoRule) {
				unavailable[tax] = true
				continue
			}
			// A period that straddles two pack versions is split by the caller.
			if begin, err := s.engine.Rate(regime, tax, method, r.ActivityGroup, first); err != nil || begin.Pack != m.Pack {
				boundary := m.Pack.EffectiveFrom.Format(time.DateOnly)
				env.Code, env.Message, env.Data = CodePeriodSpansVersions, msgSpans(boundary), map[string]any{"boundary": boundary}
				return nil, nil, CodePeriodSpansVersions
			}
			env.usePack(m.Pack)
			env.addCitation(s.rowCitation(m.Pack, m.Row.ID, m.Row.Citation, m.Row.Corpus))
			rate := *m.Row.RateBP
			amount := rules.ApplyRate(r.RevenueMinor, rate)
			line.ActivityLabel = m.Row.ActivityLabel
			line.RowIDs = append(line.RowIDs, m.Row.ID)
			if tax == "gtgt" {
				line.GTGTRateBP, line.GTGTMinor = &rate, &amount
			} else {
				line.TNCNRateBP, line.TNCNMinor = &rate, &amount
			}
		}
		lines = append(lines, line)
	}
	var missing []string
	for tax := range unavailable {
		missing = append(missing, tax)
	}
	sort.Strings(missing)
	return lines, missing, ""
}

func groupList(groups []rules.RateMatch) string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		if !seen[g.Row.ActivityGroup] {
			seen[g.Row.ActivityGroup] = true
			out = append(out, g.Row.ActivityGroup+" ("+g.Row.ActivityLabel+")")
		}
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

func sumLines(lines []TaxLine, pick func(TaxLine) *int64) *int64 {
	var total int64
	found := false
	for _, l := range lines {
		if v := pick(l); v != nil {
			total += *v
			found = true
		}
	}
	if !found {
		return nil
	}
	return &total
}

func (s *Service) calcHKD(ctx context.Context, _ *mcp.CallToolRequest, in HKDInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	if !checkPeriod(env, in.Period) {
		return s.finish(ctx, env)
	}
	last := in.Period.Last()
	env.AsOf = last.Format(time.DateOnly)
	lines, unavailable, stop := s.rateLines(env, "HKD", rules.MethodRevenueShare, in.Period, in.RevenueByGroup)
	if stop != "" {
		return s.finish(ctx, env)
	}
	data := HKDData{Lines: lines, Unavailable: append([]string{}, unavailable...), Rounding: roundingNote}
	if t, ok := s.engine.Threshold("HKD", rules.ThresholdAnnualRevenueExempt, last); ok {
		env.usePack(t.Pack)
		env.addCitation(s.rowCitation(t.Pack, t.Row.ID, t.Row.Citation, t.Row.Corpus))
		data.Threshold = &HKDThreshold{AmountMinor: t.Row.AmountMinor}
		if in.Period.Kind == rules.PeriodYear {
			var total int64
			for _, l := range lines {
				total += l.RevenueMinor
			}
			applies := total <= t.Row.AmountMinor
			data.Threshold.Applies = &applies
			if applies {
				zero := int64(0)
				for i := range data.Lines {
					if data.Lines[i].GTGTMinor != nil {
						data.Lines[i].GTGTMinor = &zero
					}
					if data.Lines[i].TNCNMinor != nil {
						data.Lines[i].TNCNMinor = &zero
					}
				}
			}
		}
	}
	if gate(env, in.Illustrative) {
		return s.finish(ctx, env)
	}
	data.Totals = HKDTotals{
		GTGTMinor: sumLines(data.Lines, func(l TaxLine) *int64 { return l.GTGTMinor }),
		TNCNMinor: sumLines(data.Lines, func(l TaxLine) *int64 { return l.TNCNMinor }),
	}
	env.Code, env.Data = CodeOK, data
	env.Message = "Đã tính theo bộ quy tắc có hiệu lực ngày " + env.AsOf + "."
	switch {
	case data.Threshold != nil && data.Threshold.Applies != nil && *data.Threshold.Applies:
		env.Message += " Doanh thu năm không vượt ngưỡng của bộ quy tắc nên số thuế là 0."
	case data.Threshold != nil && data.Threshold.Applies == nil:
		// The threshold is annual: a shorter period cannot test
		// it, so say so rather than let a quarter's figure read as owed.
		env.Message += " Ngưỡng doanh thu được xét theo năm: nếu doanh thu cả năm không vượt ngưỡng trong trường threshold thì không phải nộp số thuế này."
	}
	if len(unavailable) > 0 {
		env.Message += " Chưa có quy tắc cho: " + strings.Join(unavailable, ", ") + " — không nêu con số cho phần này; hỏi kế toán viên."
	}
	return s.finish(ctx, env)
}

func (s *Service) calcGTGT(ctx context.Context, _ *mcp.CallToolRequest, in GTGTInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	if !checkPeriod(env, in.Period) {
		return s.finish(ctx, env)
	}
	last := in.Period.Last()
	env.AsOf = last.Format(time.DateOnly)
	switch {
	case in.Method == rules.MethodDeduction && (in.KhauTru == nil || in.TrucTiep != nil):
		return s.finish(ctx, invalid(env, "khau_tru", "Phương pháp khau_tru cần đúng đối tượng khau_tru."))
	case in.Method == rules.MethodDirect && (in.TrucTiep == nil || in.KhauTru != nil):
		return s.finish(ctx, invalid(env, "truc_tiep", "Phương pháp truc_tiep cần đúng đối tượng truc_tiep."))
	case in.Method != rules.MethodDeduction && in.Method != rules.MethodDirect:
		return s.finish(ctx, invalid(env, "method", "Một trong: khau_tru, truc_tiep."))
	}
	data := GTGTData{Method: in.Method, Lines: []TaxLine{}}
	if in.Method == rules.MethodDeduction {
		k := in.KhauTru
		for _, v := range []int64{k.OutputVATMinor, k.InputVATDeductible, k.CarriedForwardMinor} {
			if v < 0 || v > maxRevenue {
				return s.finish(ctx, invalid(env, "khau_tru", "Số nguyên từ 0 đến 10^15 đồng."))
			}
		}
		m, err := s.engine.Rate("GTGT", "gtgt", rules.MethodDeduction, "", last)
		if err != nil {
			return s.finish(ctx, noMatch(env))
		}
		if begin, err := s.engine.Rate("GTGT", "gtgt", rules.MethodDeduction, "", in.Period.First()); err != nil || begin.Pack != m.Pack {
			boundary := m.Pack.EffectiveFrom.Format(time.DateOnly)
			env.Code, env.Message, env.Data = CodePeriodSpansVersions, msgSpans(boundary), map[string]any{"boundary": boundary}
			return s.finish(ctx, env)
		}
		env.usePack(m.Pack)
		env.addCitation(s.rowCitation(m.Pack, m.Row.ID, m.Row.Citation, m.Row.Corpus))
		if gate(env, in.Illustrative) {
			return s.finish(ctx, env)
		}
		net := k.OutputVATMinor - k.InputVATDeductible - k.CarriedForwardMinor
		data.PayableMinor, data.CarryForwardMinor = max(net, 0), max(-net, 0)
		env.Code, env.Data = CodeOK, data
		env.Message = "Số thuế GTGT phải nộp theo phương pháp khấu trừ, bộ quy tắc có hiệu lực ngày " + env.AsOf + "."
		return s.finish(ctx, env)
	}
	lines, _, stop := s.rateLines(env, "GTGT", rules.MethodDirect, in.Period, in.TrucTiep.RevenueByGroup)
	if stop != "" {
		return s.finish(ctx, env)
	}
	if gate(env, in.Illustrative) {
		return s.finish(ctx, env)
	}
	data.Lines, data.Rounding = lines, roundingNote
	if total := sumLines(lines, func(l TaxLine) *int64 { return l.GTGTMinor }); total != nil {
		data.PayableMinor = *total
	}
	env.Code, env.Data = CodeOK, data
	env.Message = "Số thuế GTGT phải nộp theo phương pháp trực tiếp, bộ quy tắc có hiệu lực ngày " + env.AsOf + "."
	return s.finish(ctx, env)
}

func (s *Service) deadline(ctx context.Context, _ *mcp.CallToolRequest, in DeadlineInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	if !checkPeriod(env, in.Period) {
		return s.finish(ctx, env)
	}
	env.AsOf = in.Period.Last().Format(time.DateOnly)
	switch {
	case in.EntityType != "dn" && in.EntityType != "hkd":
		return s.finish(ctx, invalid(env, "entity_type", "Một trong: dn, hkd."))
	case in.Tax != "gtgt" && in.Tax != "tncn" && in.Tax != "hkd":
		return s.finish(ctx, invalid(env, "tax", "Một trong: gtgt, tncn, hkd."))
	}
	res, err := s.engine.Deadline(rules.DeadlineQuery{Tax: in.Tax, EntityType: in.EntityType, Method: in.Method, Period: in.Period})
	switch {
	case rules.IsInvalid(err):
		return s.finish(ctx, invalid(env, "period", strings.TrimSuffix(err.Error(), ": invalid request")))
	case err != nil:
		return s.finish(ctx, noMatch(env))
	}
	env.usePack(res.Pack)
	env.addCitation(s.rowCitation(res.Pack, res.Row.ID, res.Row.Citation, res.Row.Corpus))
	if res.Shifted && res.Pack.HolidayShift != nil {
		h := res.Pack.HolidayShift
		env.addCitation(s.rowCitation(res.Pack, "holiday_shift", h.Citation, h.Corpus))
	}
	if gate(env, in.Illustrative) {
		return s.finish(ctx, env)
	}
	data := DeadlineData{
		DueDate: res.Due.Format(time.DateOnly), UnshiftedDueDate: res.Unshifted.Format(time.DateOnly),
		Shifted: res.Shifted, RowID: res.Row.ID,
	}
	if res.Holiday != nil {
		data.Holiday = &HolidayInfo{Date: res.Holiday.Date.Format(time.DateOnly), Name: res.Holiday.Name}
	}
	env.Code, env.Data = CodeOK, data
	env.Message = fmt.Sprintf("Hạn nộp hồ sơ khai thuế: %s.", data.DueDate)
	if res.Shifted {
		env.Message += fmt.Sprintf(" Hạn gốc %s trùng ngày nghỉ nên lùi sang ngày làm việc tiếp theo; ngày lễ chỉ tính theo danh sách trong bộ quy tắc.", data.UnshiftedDueDate)
	}
	return s.finish(ctx, env)
}
