# Vietnam-tax-mcp

An [MCP](https://modelcontextprotocol.io) server for **Vietnamese tax law**. It
gives Claude (or any MCP client) the original legal texts, article by article,
and calculators and checks that come from versioned rule packs rather than from
the model. Every answer cites the document, Điều, khoản and effective date, and
links the official source.

> [!WARNING]
> **Nothing in this repository has been reviewed by an accountant yet.** Every
> article in `legal/corpus` and every rule pack is `verified: false`. The
> calculators and checks refuse to give a figure from an unverified pack unless
> the caller asks for an illustrative result, and that result is labelled
> *KẾT QUẢ MINH HỌA*. This is reference information, not tax or accounting
> advice. It signs no invoice and files no return.

*Tiếng Việt: [xem bên dưới](#tiếng-việt).*

## Tools

All tools are read-only: they change nothing and store nothing.

| Tool | Title | What it does |
|---|---|---|
| `tra_cuu_van_ban` | Tìm văn bản pháp luật thuế | Search the corpus (diacritic-insensitive BM25), limited to texts in force on a date |
| `doc_dieu_khoan` | Đọc nguyên văn điều khoản | The verbatim text of an Điều, khoản or điểm, with its dates and source |
| `van_ban_ap_dung` | Văn bản áp dụng theo ngày | Which documents govern a topic on a date, and what they replaced (handles the 2026-07-01 cutover) |
| `tinh_thue_hkd` | Tính thuế hộ kinh doanh | Household-business VAT from revenue by activity group, from the `rate` packs |
| `tinh_thue_gtgt` | Tính thuế GTGT phải nộp | VAT payable by the deduction or the direct method |
| `han_nop` | Tra hạn nộp hồ sơ khai thuế | Filing due dates by entity type, tax and period, from the `deadline` packs |
| `kiem_tra_but_toan` | Kiểm tra bút toán nháp | Compare a draft posting with the rule engine's, line by line |
| `kiem_tra_hoa_don` | Kiểm tra hóa đơn điện tử nháp | Check a draft e-invoice against the required content of NĐ 254/2026/NĐ-CP |

Every result uses one envelope: `code`, `message`, `data`, `citations` (each
with `citation`, `effective_from`/`effective_to`, `verified`, `source_url`),
`verified`, `illustrative`, `label`, `as_of`, the corpus/pack/server
`versions` and a disclaimer. The full contract — input schemas, error codes,
the unverified rule — is in [`docs/DESIGN.md`](docs/DESIGN.md).

**Known gaps** (the corpus does not yet hold the text): household-business
personal income tax rates, public-holiday lists for deadline shifts, a real
TT 133/2016 posting pack, and rules before 2026-07-01 that live in
NĐ 123/2020 and NĐ 126/2020. See `docs/DESIGN.md` §17.

## Use it

### With Claude Code

```sh
make run                                    # serves http://localhost:8090/mcp
claude mcp add --transport http tax-law-vn http://localhost:8090/mcp
```

### In the Claude app

Deploy it behind HTTPS (below), then add `https://<your-host>/mcp` under
**Settings → Connectors → Add custom connector**. No OAuth is needed.

> What you type into Claude is processed under your Claude plan's terms. Use
> made-up figures; do not paste real names, tax IDs (MST) or client data.

### Build and test

Requires Go (see `go.mod`), Python 3.12+ for the corpus tools, and optionally
Docker and [golangci-lint](https://golangci-lint.run).

```sh
make test          # tools, transport, rule packs and their corpus references
make lint          # go vet + golangci-lint
make legal-check   # manifest ↔ originals ↔ sha256 ↔ corpus frontmatter
make image         # the distroless image
make help          # everything else
```

The tests read `RULES_DIR` and `LEGAL_DIR`; the Makefile sets both to this
repository's `rules/` and `legal/`.

### Deploy

`deploy/compose.yml` runs the server behind Traefik with a Let's Encrypt
certificate on port 443. `.github/workflows/deploy.yml` builds, scans and
pushes the image to GHCR on a `v*` tag, then recreates the stack on a VPS over
SSH and rolls back if the health probe fails. The secrets it needs are listed
at the top of the workflow. The server is stateless and refuses to start in
production with a `DATABASE_URL` set or fixture packs enabled.

| Variable | Default | Meaning |
|---|---|---|
| `MCP_PORT` | `8090` | Listen port |
| `MCP_PUBLIC_URL` | — (required in production) | `https://<host>`, shown on the notice page and `/healthz` |
| `LEGAL_DIR` · `RULES_DIR` | `/legal`, `/rules` in the image | Corpus and pack roots |
| `MCP_RATE_PER_MIN` | `300` | Calls per client IP per minute |
| `MCP_MAX_INFLIGHT` | `32` | Concurrent calls before `503 overloaded` |
| `MCP_ALLOWED_ORIGINS` | `https://claude.ai,https://claude.com` | `Origin` allow-list |
| `MCP_ALLOW_FIXTURE_PACKS` | `true` outside production | Whether test packs load |
| `STATS_USER` · `STATS_PASSWORD` | — | HTTP Basic for `/internal/stats`; empty password = not mounted |

## The legal corpus

`legal/` holds the original texts, fetched **only from official portals**
(congbao.chinhphu.vn, vbpl.vn), stored byte for byte in `legal/originals` with
their sha256, and split into one Markdown file per article in `legal/corpus`.
Secondary sites are never used as a source. `legal/corpus/INDEX.md` lists the
documents; `legal/README.md` explains the format and `scripts/legal_fetch.py`
rebuilds everything.

Current documents include Luật Quản lý thuế 108/2025/QH15, NĐ 252/2026/NĐ-CP
and TT 89/2026/TT-BTC (tax administration), NĐ 254/2026/NĐ-CP and
TT 91/2026/TT-BTC (e-invoices), NĐ 68/2026/NĐ-CP, NĐ 141/2026/NĐ-CP,
TT 18/2026/TT-BTC and TT 50/2026/TT-BTC (household businesses), Luật Thuế GTGT
48/2024/QH15 with its amendments and NĐ 181/2025/NĐ-CP, NQ 198/2025/QH15 and
TT 133/2016/TT-BTC.

Normative legal documents are not protected by copyright in Vietnam (Luật Sở
hữu trí tuệ 50/2005/QH11, Điều 15 khoản 2), so the texts are redistributed as
published.

## How verification works

An accountant reviews an article or a pack against the original and sets
`verified: true` with `verified_by: "<name> — <date>"`. A rebuild of the corpus
keeps a sign-off while the text is unchanged and drops it when the text
changes. A result is `verified` only when every citation and every pack it used
is. Contributions of reviews are welcome — see `CONTRIBUTING.md`.

## Privacy

The server keeps nothing about callers. Each call writes one log line with the
tool name, result code, latency and versions — never an argument, a result or
an IP address. There is no database, no session and no third-party telemetry.

## License

[MIT](LICENSE) for the code and the rule packs. The legal texts are public
documents of the State of Vietnam.

---

## Tiếng Việt

Máy chủ MCP cho **pháp luật thuế Việt Nam**: cung cấp nguyên văn văn bản pháp
luật theo từng Điều, cùng các công cụ tính thuế và kiểm tra lấy từ bộ quy tắc
có phiên bản, không do mô hình AI tự suy luận. Mọi câu trả lời đều trích dẫn số
văn bản, Điều, khoản, ngày hiệu lực và đường dẫn tới nguồn chính thức.

> [!WARNING]
> **Toàn bộ nội dung chưa được kế toán viên đối chiếu với bản gốc**
> (`verified: false`). Công cụ tính chỉ đưa ra con số khi người dùng yêu cầu
> kết quả minh họa, và kết quả đó được ghi rõ *KẾT QUẢ MINH HỌA*. Đây là thông
> tin tham khảo, không phải dịch vụ kế toán hay tư vấn thuế. Hệ thống không ký
> hóa đơn và không nộp tờ khai thay bạn.

**Cách dùng với Claude:** chạy `make run`, rồi thêm
`http://localhost:8090/mcp` vào Claude Code
(`claude mcp add --transport http tax-law-vn http://localhost:8090/mcp`), hoặc
triển khai lên máy chủ có HTTPS và thêm `https://<tên-miền>/mcp` trong ứng
dụng Claude: **Cài đặt → Trình kết nối → Thêm trình kết nối tùy chỉnh**.

**Dữ liệu:** máy chủ không lưu nội dung bạn gửi. Những gì bạn gõ vào Claude
được xử lý theo điều khoản gói Claude của bạn. Chỉ dùng số liệu giả định;
không dán tên, mã số thuế hay dữ liệu thật của khách hàng.

**Văn bản pháp luật** chỉ lấy từ cổng thông tin chính thức (Công báo,
vbpl.vn), lưu nguyên bản kèm mã sha256. Văn bản quy phạm pháp luật không thuộc
đối tượng bảo hộ quyền tác giả (Luật Sở hữu trí tuệ, Điều 15).

**Đóng góp:** rất hoan nghênh kế toán viên giúp đối chiếu điều khoản và bộ quy
tắc với bản gốc — xem `CONTRIBUTING.md`.
