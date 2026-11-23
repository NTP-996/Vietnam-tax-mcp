package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NTP-996/Vietnam-tax-mcp/internal/rules"
)

// Instructions are sent on initialize.
const Instructions = `Máy chủ này cung cấp văn bản pháp luật thuế Việt Nam lấy từ cổng thông tin chính thức và các phép tính, phép kiểm tra theo bộ quy tắc có phiên bản. Khi dùng máy chủ này:
1. Mọi khẳng định về thuế, hóa đơn hay kế toán phải kèm trích dẫn đúng trường "citation" mà công cụ trả về (số văn bản, Điều, khoản, điểm, ngày hiệu lực) và đường dẫn "source_url".
2. Không bao giờ nêu thuế suất, tỷ lệ, ngưỡng doanh thu, số tiền hay hạn nộp nếu không có công cụ nào trả về con số đó. Không dùng trí nhớ hay kiến thức chung để thay thế.
3. Khi người dùng hỏi nội dung một điều khoản, trích nguyên văn trường "text"; không diễn giải thay cho văn bản.
4. Nếu "verified" là false, nói rõ: "Nội dung chưa được kế toán trưởng đối chiếu với bản gốc." Nếu "illustrative" là true, nhắc lại nguyên văn trường "label".
5. Nếu kết quả là "no_match", "not_in_force" hoặc "unverified_pack", hoặc máy chủ không phản hồi, nói rằng chưa có quy định áp dụng được xác minh và đề nghị người dùng hỏi kế toán viên. Không tự suy đoán câu trả lời.
6. Ngày giao dịch quyết định văn bản áp dụng: luôn truyền "as_of" hoặc ngày giao dịch khi người dùng nêu ngày; dùng "van_ban_ap_dung" khi không chắc văn bản nào có hiệu lực.
7. Đây là thông tin tham khảo, không phải dịch vụ kế toán hay tư vấn thuế. Hệ thống không ký hóa đơn và không nộp tờ khai thay người dùng.
8. Nhắc người dùng chỉ dùng số liệu giả định, không dán tên, mã số thuế hay dữ liệu thật của khách hàng.`

// RequestIDHeader carries the HTTP request id into the tool call (the HTTP
// layer sets it on the inbound request; the SDK exposes the headers).
const RequestIDHeader = "X-Request-Id"

// callTimeout bounds one tool call; every tool is in memory.
const callTimeout = 30 * time.Second

// Options wires a Service.
type Options struct {
	Corpus      *Corpus
	Engine      *rules.Engine
	Version     string // server_version: the image tag
	ProductName string
	PublicURL   string
	Logger      *slog.Logger     // the tool_call lines; nil = slog.Default()
	Now         func() time.Time // nil = time.Now
	Location    *time.Location   // business dates; nil = Asia/Ho_Chi_Minh
}

// Service holds the loaded corpus and packs and the MCP server over them.
type Service struct {
	corpus  *Corpus
	engine  *rules.Engine
	version string
	product string
	public  string
	log     *slog.Logger
	now     func() time.Time
	loc     *time.Location
	stats   *Stats
	server  *mcp.Server
}

// New builds the MCP server and registers the eight tools.
func New(o Options) *Service {
	s := &Service{
		corpus: o.Corpus, engine: o.Engine, version: o.Version, product: o.ProductName, public: o.PublicURL,
		log: o.Logger, now: o.Now, loc: o.Location, stats: NewStats(),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.loc == nil {
		loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
		if err != nil {
			loc = time.FixedZone("ICT", 7*3600)
		}
		s.loc = loc
	}
	if s.product == "" {
		s.product = "Tra cứu luật thuế"
	}
	// The SDK's own logger stays nil (discard): the tool_call line of
	// telemetry.go is the only log a call produces.
	s.server = mcp.NewServer(&mcp.Implementation{
		Name: "tax-law-vn", Title: s.product, Version: s.version, WebsiteURL: s.public,
	}, &mcp.ServerOptions{Instructions: Instructions})
	s.server.AddReceivingMiddleware(s.observe)
	s.registerLookup()
	s.registerCalc()
	s.registerCheck()
	return s
}

// MCP is the server to mount on a transport.
func (s *Service) MCP() *mcp.Server { return s.server }

// Stats are the in-memory counters.
func (s *Service) Stats() *Stats { return s.stats }

// Corpus is the loaded corpus.
func (s *Service) Corpus() *Corpus { return s.corpus }

// Engine is the loaded rule engine.
func (s *Service) Engine() *rules.Engine { return s.engine }

// Version is the server version.
func (s *Service) Version() string { return s.version }

// today is the current business date in Asia/Ho_Chi_Minh, at UTC midnight
// (the corpus and the packs compare civil dates).
func (s *Service) today() time.Time {
	y, m, d := s.now().In(s.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

type callKey struct{}

// callRecord carries the envelope from the handler to the middleware, so
// the log line can name the code without reading the result body.
type callRecord struct{ env *Envelope }

func (s *Service) observe(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		tool, requestID := "unknown", ""
		if call, ok := req.(*mcp.CallToolRequest); ok {
			if call.Params != nil && knownTool(call.Params.Name) {
				tool = call.Params.Name
			}
			if call.Extra != nil && call.Extra.Header != nil {
				requestID = call.Extra.Header.Get(RequestIDHeader)
			}
		}
		rec := &callRecord{}
		ctx = context.WithValue(ctx, callKey{}, rec)
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		start := time.Now()
		res, err := next(ctx, method, req)
		code := CodeInternal
		switch r := res.(type) {
		case *mcp.CallToolResult:
			if rec.env == nil && r.IsError {
				// The SDK's schema check refused the arguments before the
				// handler ran: answer with the same envelope.
				env := &Envelope{Code: CodeInvalidArgument, Message: msgInvalid(sdkDetail(r), "")}
				r2, _, _ := s.finish(ctx, env)
				r2.StructuredContent = env
				r2.Content = []mcp.Content{&mcp.TextContent{Text: mustJSON(env)}}
				res = r2
			}
			if rec.env != nil {
				code = rec.env.Code
			}
		default:
			if err != nil {
				code = "protocol_error"
			}
		}
		s.record(requestID, tool, code, time.Since(start), rec.env)
		return res, err
	}
}

// sdkDetail is the SDK's validation message without its "validating" prefix.
func sdkDetail(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			msg := strings.TrimPrefix(t.Text, `validating "arguments": `)
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return msg
		}
	}
	return "arguments"
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"code":"internal"}`
	}
	return string(b)
}

// finish completes the envelope (disclaimer, versions, verified) and builds
// the MCP result: isError for the error codes of DESIGN.md §6.
func (s *Service) finish(ctx context.Context, env *Envelope) (*mcp.CallToolResult, *Envelope, error) {
	env.Disclaimer = Disclaimer
	if env.Citations == nil {
		env.Citations = []Citation{}
	}
	if env.AsOf == "" {
		env.AsOf = s.today().Format(time.DateOnly)
	}
	env.Versions = Versions{Corpus: s.corpus.Version, Packs: []string{}, Server: s.version}
	for _, p := range env.packs {
		env.Versions.Packs = append(env.Versions.Packs, p.Version)
	}
	env.Verified = len(env.Citations) > 0 && !env.unverified()
	for _, c := range env.Citations {
		if !c.Verified {
			env.Verified = false
		}
	}
	if rec, ok := ctx.Value(callKey{}).(*callRecord); ok {
		rec.env = env
	}
	return &mcp.CallToolResult{IsError: isErrorCode(env.Code)}, env, nil
}

// gate applies the unverified rule: an unverified pack gives no
// figure unless the caller asked for an illustrative result, which is then
// labeled. It reports whether the answer must stop here.
func gate(env *Envelope, illustrative bool) bool {
	if !env.unverified() {
		return false
	}
	if !illustrative {
		env.Code, env.Message, env.Data = CodeUnverifiedPack, msgUnverified, nil
		return true
	}
	env.Illustrative = true
	label := IllustrativeLabel
	env.Label = &label
	return false
}

// invalid fills an invalid_argument envelope.
func invalid(env *Envelope, field, allowed string) *Envelope {
	env.Code, env.Message, env.Data = CodeInvalidArgument, msgInvalid(field, allowed), nil
	return env
}

// noMatch fills a no_match envelope.
func noMatch(env *Envelope) *Envelope {
	env.Code, env.Message = CodeNoMatch, msgNoMatch
	return env
}

// parseDay parses a YYYY-MM-DD argument; an empty value is the default.
func parseDay(v string, def time.Time) (time.Time, bool) {
	if v == "" {
		return def, true
	}
	t, err := time.Parse(time.DateOnly, v)
	return t, err == nil
}

// tools are the eight tool names (telemetry logs only these).
var tools = []string{
	"tra_cuu_van_ban", "doc_dieu_khoan", "van_ban_ap_dung",
	"tinh_thue_hkd", "tinh_thue_gtgt", "han_nop",
	"kiem_tra_but_toan", "kiem_tra_hoa_don",
}

func knownTool(name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

// annotations are the same on every tool: read-only, idempotent, closed world.
func annotations(title string) *mcp.ToolAnnotations {
	no := false
	return &mcp.ToolAnnotations{
		Title: title, ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no,
	}
}

// descriptionTail ends every tool description.
const descriptionTail = " Chỉ dùng số liệu giả định; không dán dữ liệu thật của khách hàng."
