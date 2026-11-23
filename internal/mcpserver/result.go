package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// Result codes.
const (
	CodeOK                  = "ok"
	CodeNoMatch             = "no_match"
	CodeNotInForce          = "not_in_force"
	CodeUnverifiedPack      = "unverified_pack"
	CodePeriodSpansVersions = "period_spans_versions"
	CodeInvalidArgument     = "invalid_argument"
	CodeInternal            = "internal"
)

// isErrorCode reports the codes that set isError on the MCP result.
func isErrorCode(code string) bool {
	switch code {
	case CodeNotInForce, CodeUnverifiedPack, CodePeriodSpansVersions, CodeInvalidArgument, CodeInternal:
		return true
	}
	return false
}

// Fixed strings.
const (
	Disclaimer        = "Thông tin tham khảo từ văn bản pháp luật và bộ quy tắc có phiên bản, không phải dịch vụ kế toán hay tư vấn thuế. Hệ thống không ký hóa đơn và không nộp tờ khai thay bạn. Hãy kiểm tra với kế toán viên trước khi kê khai."
	IllustrativeLabel = "KẾT QUẢ MINH HỌA — chưa được kế toán trưởng xác minh, không dùng để kê khai."
	msgNoMatch        = "Không tìm thấy quy định phù hợp trong kho văn bản. Vui lòng hỏi kế toán viên."
	msgUnverified     = "Bộ quy tắc này chưa được xác minh nên chưa đưa ra con số. Bạn có thể yêu cầu kết quả minh họa."
	msgInternal       = "Đã có lỗi phía máy chủ; vui lòng thử lại hoặc hỏi kế toán viên."
)

func msgNotInForce(asOf string, from time.Time, to *time.Time) string {
	until := "nay"
	if to != nil {
		until = to.Format(time.DateOnly)
	}
	return fmt.Sprintf("Văn bản này không có hiệu lực vào ngày %s (hiệu lực từ %s đến %s).", asOf, from.Format(time.DateOnly), until)
}

func msgSpans(boundary string) string {
	return fmt.Sprintf("Kỳ này thuộc hai phiên bản quy định; vui lòng tách kỳ tại ngày %s.", boundary)
}

func msgInvalid(field, allowed string) string {
	return strings.TrimSpace(fmt.Sprintf("Thông tin chưa hợp lệ: %s. %s", field, allowed))
}

// Citation is one source of an answer.
type Citation struct {
	ID            string  `json:"id"`
	Citation      string  `json:"citation"`
	Doc           string  `json:"doc"`
	Article       *string `json:"article"`
	Khoan         *int    `json:"khoan"`
	Diem          *string `json:"diem"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
	Verified      bool    `json:"verified"`
	VerifiedBy    *string `json:"verified_by"`
	SourceURL     string  `json:"source_url"`
}

// Versions names the corpus, the packs and the server behind an answer.
type Versions struct {
	Corpus string   `json:"corpus"`
	Packs  []string `json:"packs"`
	Server string   `json:"server"`
}

// Envelope is every tool's result.
type Envelope struct {
	Code         string     `json:"code"`
	Message      string     `json:"message"`
	Data         any        `json:"data"`
	Citations    []Citation `json:"citations"`
	Verified     bool       `json:"verified"`
	Illustrative bool       `json:"illustrative"`
	Label        *string    `json:"label"`
	AsOf         string     `json:"as_of"`
	Versions     Versions   `json:"versions"`
	Disclaimer   string     `json:"disclaimer"`

	packs []*rules.Pack // the packs the answer used (verified flag, versions)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func datePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

// articleCitation cites a whole article, or one khoản / điểm of it.
func articleCitation(a *Article, khoan int, diem string) Citation {
	text := a.Doc.Cite + ", " + a.Label
	c := Citation{
		ID: a.ID, Doc: a.Doc.Cite, Article: strPtr(a.Label),
		EffectiveFrom: a.EffectiveFrom.Format(time.DateOnly), EffectiveTo: datePtr(a.EffectiveTo),
		Verified: a.Verified, VerifiedBy: strPtr(a.VerifiedBy), SourceURL: a.SourceURL,
	}
	if khoan > 0 {
		k := khoan
		c.Khoan = &k
		text += fmt.Sprintf(", khoản %d", khoan)
	}
	if diem != "" {
		c.Diem = strPtr(diem)
		text += ", điểm " + diem
	}
	text += " — effective " + c.EffectiveFrom
	if c.EffectiveTo != nil {
		text += " — until " + *c.EffectiveTo
	}
	c.Citation = text
	return c
}

// documentCitation cites a whole document (van_ban_ap_dung).
func documentCitation(d *Document) Citation {
	c := Citation{
		ID: d.ID, Doc: d.Cite, EffectiveFrom: d.EffectiveFrom.Format(time.DateOnly),
		EffectiveTo: datePtr(d.EffectiveTo), Verified: d.Verified(), SourceURL: d.SourceURL,
	}
	c.Citation = d.Cite + " — effective " + c.EffectiveFrom
	if c.EffectiveTo != nil {
		c.Citation += " — until " + *c.EffectiveTo
	}
	return c
}

// rowCitation cites a pack row: the row's own citation string, with the dates,
// verification and source of the corpus article it points at. A fixture row
// has no article and is never verified.
func (s *Service) rowCitation(p *rules.Pack, rowID, citation, corpus string) Citation {
	if a, ok := s.corpus.Article(corpus); ok {
		c := articleCitation(a, 0, "")
		c.Citation = citation
		return c
	}
	return Citation{
		ID: p.Version + "/" + rowID, Citation: citation, Doc: p.LegalDoc, Article: strPtr(p.Article),
		EffectiveFrom: p.EffectiveFrom.Format(time.DateOnly), SourceURL: "",
	}
}

// addCitation appends a citation unless one with the same text is present.
func (e *Envelope) addCitation(c Citation) {
	for _, have := range e.Citations {
		if have.Citation == c.Citation && have.ID == c.ID {
			return
		}
	}
	e.Citations = append(e.Citations, c)
}

// usePack records a pack the answer depends on.
func (e *Envelope) usePack(p *rules.Pack) {
	for _, have := range e.packs {
		if have == p {
			return
		}
	}
	e.packs = append(e.packs, p)
}

// unverified reports whether any pack the answer used lacks a sign-off.
func (e *Envelope) unverified() bool {
	for _, p := range e.packs {
		if !p.Verified {
			return true
		}
	}
	return false
}
