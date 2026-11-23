package mcpserver

import (
	"bytes"
	"embed"
	"html/template"
	"time"
)

//go:embed templates/notice.html
var templatesFS embed.FS

var noticeTemplate = template.Must(template.ParseFS(templatesFS, "templates/notice.html"))

type noticeDoc struct {
	Cite, Title, From, To, Status, SourceURL string
}

type noticeTool struct{ Name, Title string }

// noticeTitles are the tools' Vietnamese titles, in the order of DESIGN.md §5.
var noticeTitles = []noticeTool{
	{"tra_cuu_van_ban", "Tìm văn bản pháp luật thuế"},
	{"doc_dieu_khoan", "Đọc nguyên văn điều khoản"},
	{"van_ban_ap_dung", "Văn bản áp dụng theo ngày"},
	{"tinh_thue_hkd", "Tính thuế hộ kinh doanh"},
	{"tinh_thue_gtgt", "Tính thuế GTGT phải nộp"},
	{"han_nop", "Tra hạn nộp hồ sơ khai thuế"},
	{"kiem_tra_but_toan", "Kiểm tra bút toán nháp"},
	{"kiem_tra_hoa_don", "Kiểm tra hóa đơn điện tử nháp"},
}

// Notice renders the notice page once; it changes only with a
// deploy.
func (s *Service) Notice() ([]byte, error) {
	base := s.public
	if base == "" {
		base = "http://localhost:8090"
	}
	docs := make([]noticeDoc, 0, len(s.corpus.Docs))
	for _, d := range s.corpus.Docs {
		nd := noticeDoc{Cite: d.Cite, Title: d.Title, From: d.EffectiveFrom.Format(time.DateOnly), Status: "chưa xác minh", SourceURL: d.SourceURL}
		if d.EffectiveTo != nil {
			nd.To = d.EffectiveTo.Format(time.DateOnly)
		}
		if d.Verified() {
			nd.Status = "đã xác minh"
		}
		docs = append(docs, nd)
	}
	var buf bytes.Buffer
	err := noticeTemplate.Execute(&buf, map[string]any{
		"Product": s.product, "MCPURL": base + "/mcp", "Tools": noticeTitles, "Disclaimer": Disclaimer,
		"CorpusVersion": s.corpus.Version, "ServerVersion": s.version, "Docs": docs,
	})
	return buf.Bytes(), err
}
