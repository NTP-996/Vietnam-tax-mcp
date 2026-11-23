package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTextRunes bounds the article text one result carries, so text and
// structured content together stay under 100,000 characters.
const maxTextRunes = 40000

// SearchInput is tra_cuu_van_ban's arguments.
type SearchInput struct {
	Query string `json:"query"`
	AsOf  string `json:"as_of,omitempty"`
	Doc   string `json:"doc,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// SearchHit is one hit of tra_cuu_van_ban.
type SearchHit struct {
	ID            string  `json:"id"`
	Doc           string  `json:"doc"`
	Article       string  `json:"article"`
	Title         string  `json:"title"`
	Snippet       string  `json:"snippet"`
	Score         float64 `json:"score"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
	Verified      bool    `json:"verified"`
	SourceURL     string  `json:"source_url"`
}

// ArticleInput is doc_dieu_khoan's arguments.
type ArticleInput struct {
	Doc   string `json:"doc"`
	Dieu  int    `json:"dieu"`
	Khoan int    `json:"khoan,omitempty"`
	Diem  string `json:"diem,omitempty"`
	AsOf  string `json:"as_of,omitempty"`
}

// ArticleData is doc_dieu_khoan's data.
type ArticleData struct {
	ID        string  `json:"id"`
	Doc       string  `json:"doc"`
	Dieu      int     `json:"dieu"`
	Khoan     *int    `json:"khoan"`
	Diem      *string `json:"diem"`
	Title     string  `json:"title"`
	Text      string  `json:"text"`
	Truncated bool    `json:"truncated"`
	NextKhoan *int    `json:"next_khoan"`
	SHA256    string  `json:"sha256"`
}

// ApplicableInput is van_ban_ap_dung's arguments.
type ApplicableInput struct {
	Topic string `json:"topic"`
	AsOf  string `json:"as_of"`
}

// DocumentInfo is one document of van_ban_ap_dung.
type DocumentInfo struct {
	ID            string   `json:"id"`
	Doc           string   `json:"doc"`
	Title         string   `json:"title"`
	EffectiveFrom string   `json:"effective_from"`
	EffectiveTo   *string  `json:"effective_to"`
	Replaces      []string `json:"replaces"`
	Note          string   `json:"note"`
	Verified      bool     `json:"verified"`
	SourceURL     string   `json:"source_url"`
}

func (s *Service) registerLookup() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "tra_cuu_van_ban",
		Title:       "Tìm văn bản pháp luật thuế",
		Description: "Tìm điều khoản trong kho văn bản thuế, hóa đơn, kế toán lấy từ cổng thông tin chính thức. Chỉ tìm văn bản có hiệu lực vào ngày as_of (mặc định hôm nay). Trả về đoạn trích, mã điều và trích dẫn." + descriptionTail,
		InputSchema: inputSchema("tra_cuu_van_ban"),
		Annotations: annotations("Tìm văn bản pháp luật thuế"),
	}, s.search)
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "doc_dieu_khoan",
		Title:       "Đọc nguyên văn điều khoản",
		Description: "Đọc nguyên văn một Điều, khoản hoặc điểm của văn bản trong kho, kèm ngày hiệu lực, nguồn và trạng thái xác minh. Văn bản không có hiệu lực vào ngày as_of thì chỉ trả về khoảng hiệu lực." + descriptionTail,
		InputSchema: inputSchema("doc_dieu_khoan"),
		Annotations: annotations("Đọc nguyên văn điều khoản"),
	}, s.readArticle)
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "van_ban_ap_dung",
		Title:       "Văn bản áp dụng theo ngày",
		Description: "Liệt kê văn bản trong kho điều chỉnh một chủ đề (hoa_don, quan_ly_thue, gtgt, hkd, ke_toan) có hiệu lực vào một ngày, kèm văn bản đã hết hiệu lực và sắp có hiệu lực, để xử lý mốc chuyển tiếp 01/07/2026." + descriptionTail,
		InputSchema: inputSchema("van_ban_ap_dung"),
		Annotations: annotations("Văn bản áp dụng theo ngày"),
	}, s.applicable)
}

func (s *Service) search(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	asOf, ok := parseDay(in.AsOf, s.today())
	if !ok {
		return s.finish(ctx, invalid(env, "as_of", "Định dạng YYYY-MM-DD."))
	}
	env.AsOf = asOf.Format(time.DateOnly)
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Query)); n < 2 || n > 200 {
		return s.finish(ctx, invalid(env, "query", "Từ 2 đến 200 ký tự."))
	}
	limit := in.Limit
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 20 {
		return s.finish(ctx, invalid(env, "limit", "Từ 1 đến 20."))
	}
	var doc *Document
	if in.Doc != "" {
		d, ok := s.corpus.FindDoc(in.Doc)
		if !ok {
			return s.finish(ctx, noMatch(env))
		}
		doc = d
	}
	hits := s.corpus.Search(in.Query, asOf, doc, limit)
	if len(hits) == 0 {
		return s.finish(ctx, noMatch(env))
	}
	out := make([]SearchHit, 0, len(hits))
	for _, h := range hits {
		a := h.Article
		out = append(out, SearchHit{
			ID: a.ID, Doc: a.Doc.Cite, Article: a.Label, Title: a.Title, Snippet: h.Snippet,
			Score: float64(int(h.Score*1000)) / 1000, EffectiveFrom: a.EffectiveFrom.Format(time.DateOnly),
			EffectiveTo: datePtr(a.EffectiveTo), Verified: a.Verified, SourceURL: a.SourceURL,
		})
		env.addCitation(articleCitation(a, 0, ""))
	}
	env.Code, env.Message = CodeOK, fmt.Sprintf("Tìm thấy %d điều khoản có hiệu lực vào ngày %s.", len(out), env.AsOf)
	env.Data = map[string]any{"hits": out}
	return s.finish(ctx, env)
}

func (s *Service) readArticle(ctx context.Context, _ *mcp.CallToolRequest, in ArticleInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	asOf, ok := parseDay(in.AsOf, s.today())
	if !ok {
		return s.finish(ctx, invalid(env, "as_of", "Định dạng YYYY-MM-DD."))
	}
	env.AsOf = asOf.Format(time.DateOnly)
	switch {
	case in.Dieu < 1:
		return s.finish(ctx, invalid(env, "dieu", "Số Điều từ 1."))
	case in.Diem != "" && in.Khoan == 0:
		return s.finish(ctx, invalid(env, "diem", "Điểm cần đi kèm khoản."))
	case in.Diem != "" && (utf8.RuneCountInString(in.Diem) != 1 || !strings.Contains(diemOrder, in.Diem)):
		return s.finish(ctx, invalid(env, "diem", "Một chữ cái a, b, c, d, đ, e…"))
	}
	doc, ok := s.corpus.FindDoc(in.Doc)
	if !ok {
		return s.finish(ctx, noMatch(env))
	}
	a, ok := s.corpus.Article(fmt.Sprintf("%s/dieu-%d", doc.ID, in.Dieu))
	if !ok {
		return s.finish(ctx, noMatch(env))
	}
	if !inRange(asOf, a.EffectiveFrom, a.EffectiveTo) {
		env.Code, env.Message = CodeNotInForce, msgNotInForce(env.AsOf, a.EffectiveFrom, a.EffectiveTo)
		env.Data = map[string]any{"effective_from": a.EffectiveFrom.Format(time.DateOnly), "effective_to": datePtr(a.EffectiveTo)}
		env.addCitation(articleCitation(a, 0, ""))
		return s.finish(ctx, env)
	}
	data := ArticleData{ID: a.ID, Doc: doc.Cite, Dieu: a.Number, Title: a.Title, SHA256: a.SHA256}
	var text string
	switch {
	case in.Khoan > 0:
		k, ok := findKhoan(a, in.Khoan)
		if !ok {
			return s.finish(ctx, noMatch(env))
		}
		kn := in.Khoan
		data.Khoan, text = &kn, k.Text
		if in.Diem != "" {
			d, ok := findDiem(k, in.Diem)
			if !ok {
				return s.finish(ctx, noMatch(env))
			}
			data.Diem, text = strPtr(in.Diem), d.Text
		}
	default:
		text, data.Truncated, data.NextKhoan = truncateAtKhoan(a)
	}
	data.Text = text
	env.addCitation(articleCitation(a, in.Khoan, in.Diem))
	env.Code, env.Message, env.Data = CodeOK, "Nguyên văn "+env.Citations[0].Citation+".", data
	return s.finish(ctx, env)
}

func findKhoan(a *Article, n int) (Khoan, bool) {
	for _, k := range a.Khoan {
		if k.Number == n {
			return k, true
		}
	}
	return Khoan{}, false
}

func findDiem(k Khoan, letter string) (Diem, bool) {
	for _, d := range k.Diem {
		if d.Letter == letter {
			return d, true
		}
	}
	return Diem{}, false
}

// truncateAtKhoan returns the whole article, or as many whole khoản as fit
// and the number of the first khoản left out.
func truncateAtKhoan(a *Article) (string, bool, *int) {
	if utf8.RuneCountInString(a.Text) <= maxTextRunes || len(a.Khoan) < 2 {
		if utf8.RuneCountInString(a.Text) <= maxTextRunes {
			return a.Text, false, nil
		}
		runes := []rune(a.Text)
		return string(runes[:maxTextRunes]), true, nil
	}
	var b strings.Builder
	used := 0
	for i, k := range a.Khoan {
		n := utf8.RuneCountInString(k.Text) + 2
		if used+n > maxTextRunes && i > 0 {
			next := k.Number
			return strings.TrimSpace(b.String()), true, &next
		}
		b.WriteString(k.Text)
		b.WriteString("\n\n")
		used += n
	}
	return strings.TrimSpace(b.String()), false, nil
}

func (s *Service) applicable(ctx context.Context, _ *mcp.CallToolRequest, in ApplicableInput) (*mcp.CallToolResult, *Envelope, error) {
	env := &Envelope{}
	asOf, ok := parseDay(in.AsOf, time.Time{})
	if !ok || in.AsOf == "" {
		return s.finish(ctx, invalid(env, "as_of", "Định dạng YYYY-MM-DD."))
	}
	env.AsOf = asOf.Format(time.DateOnly)
	switch in.Topic {
	case "hoa_don", "quan_ly_thue", "gtgt", "hkd", "ke_toan":
	default:
		return s.finish(ctx, invalid(env, "topic", "Một trong: hoa_don, quan_ly_thue, gtgt, hkd, ke_toan."))
	}
	inForce, superseded, upcoming := []DocumentInfo{}, []DocumentInfo{}, []DocumentInfo{}
	for _, d := range s.corpus.Docs {
		if !hasTopic(d, in.Topic) {
			continue
		}
		info := DocumentInfo{
			ID: d.ID, Doc: d.Cite, Title: d.Title, EffectiveFrom: d.EffectiveFrom.Format(time.DateOnly),
			EffectiveTo: datePtr(d.EffectiveTo), Replaces: d.Replaces, Note: d.Note, Verified: d.Verified(), SourceURL: d.SourceURL,
		}
		switch {
		case d.InForce(asOf):
			inForce = append(inForce, info)
			env.addCitation(documentCitation(d))
		case d.EffectiveTo != nil && d.EffectiveTo.Before(asOf):
			superseded = append(superseded, info)
		default:
			upcoming = append(upcoming, info)
		}
	}
	env.Data = map[string]any{"documents": inForce, "superseded": superseded, "upcoming": upcoming}
	if len(inForce) == 0 {
		// The lists stay: "upcoming" shows the documents that take over later
		// (a date before the 2026-07-01 cutover for hoa_don, for example).
		return s.finish(ctx, noMatch(env))
	}
	env.Code = CodeOK
	env.Message = fmt.Sprintf("%d văn bản trong kho có hiệu lực vào ngày %s cho chủ đề %s.", len(inForce), env.AsOf, in.Topic)
	return s.finish(ctx, env)
}

func hasTopic(d *Document, topic string) bool {
	for _, t := range d.Topics {
		if t == topic {
			return true
		}
	}
	return false
}
