# Design notes — Vietnamese tax-law MCP server

These notes are the contract the code implements: a public, stateless,
read-only MCP server over the official Vietnamese legal corpus in `legal/` and
the versioned rule packs in `rules/`. Code comments cite them by section
(`DESIGN.md §5.4`). Where the code and these notes disagree, the code is right
and these notes are fixed in the same change. §17 records where the build
settled or changed §§2–16.

## 1 · Context and constraints

- **The model never decides tax.** Calculators and checks are rule-engine
  output with a citation and an effective date; look-ups return the stored
  text verbatim. The server instructions forbid the model to state a rate,
  threshold or deadline that no tool returned.
- **Every rule cites its article and effective date**, in the format
  `<doc>, Điều <n>[, khoản <k>][, điểm <x>] — effective <date>`.
- **Nothing is verified until an accountant signs it off.** Every corpus
  article and pack carries `verified: false` until a reviewing accountant
  checks it against the original. Calculators refuse a figure from an
  unverified pack unless the caller asks for an illustrative result, which is
  then labelled.
- **Public law only, stateless.** No database, no volume, no session store,
  nothing about the caller kept; a draft to check exists in memory for one
  call. Authless, with a rate limit.
- **The server signs nothing and files nothing.** It is reference
  information, not an accounting service or tax advice.

## 2 · Layout and configuration

| Path | What |
|---|---|
| `cmd/mcp/main.go` | Config, corpus and pack load, gin engine, `/mcp`, `/healthz`, `/`, `/internal/stats`; graceful shutdown; `-healthcheck` flag |
| `internal/mcpserver/server.go` | Builds the `*mcp.Server` (implementation name, version = image tag, instructions §7) and registers the tools |
| `internal/mcpserver/tools_*.go` | One file per tool group: `lookup` (§5.1–5.3), `calc` (§5.4–5.6), `check` (§5.7–5.8) |
| `internal/mcpserver/result.go` | The result envelope (§5.0), error codes (§6), the disclaimer and the illustrative label |
| `internal/mcpserver/corpus.go` | Loads `legal/corpus/**.md` and the manifest; computes the corpus version (§3) |
| `internal/mcpserver/search.go` | Diacritic-folding BM25 (§10) |
| `internal/mcpserver/notice.go` | The notice page (§7.3), embedded HTML |
| `internal/mcpserver/telemetry.go` | The per-call log line and the in-memory counters (§9) |
| `internal/rules/` | Pack kinds `rate`, `deadline`, `invoice_fields` (§4) — `engine.go` (the `kind` field, `validate`), `kinds.go` (the new kinds, their look-ups and `CheckCorpus`), `regression.go` (tables per kind) |
| `internal/mcpserver/http.go` · `schemas.go` · `templates/notice.html` | The gin engine and its chain (§8); the input schemas of §5 as JSON; the notice page |
| `rules/packs/<document>/<article>.yaml` · `rules/tests/*.yaml` | Real packs written from the corpus, with their tables |
| `Dockerfile` | Builds `/mcp`; copies `legal/corpus` and `legal/manifest.yaml` to `/legal` and `rules` to `/rules` |
| `deploy/compose.yml` · `.github/workflows/deploy.yml` | Deployment (§11) |
| `Makefile` | `run`, `test`, `lint`, `legal-check`; it sets `RULES_DIR` and `LEGAL_DIR` so the corpus references are checked |

Configuration, read through `internal/config` (`config.LoadMCP` in `mcp.go`):

| Key | Default | Meaning |
|---|---|---|
| `MCP_PORT` | `8090` | Listen port |
| `LEGAL_DIR` | `/legal`, else `./legal` | Corpus root (the `FindDir` pattern of `internal/rules`) |
| `MCP_PUBLIC_URL` | — (required in production) | `https://<host>` — used on the notice page and in `/healthz` |
| `MCP_PRODUCT_NAME` | `Tra cứu luật thuế` | The name on the notice page and in the server implementation title |
| `MCP_RATE_PER_MIN` | `300` | Calls per client IP per minute (§8) |
| `MCP_MAX_INFLIGHT` | `32` | Concurrent tool calls before 503 `overloaded` |
| `MCP_ALLOWED_ORIGINS` | `https://claude.ai,https://claude.com` (+ `http://localhost:*` outside production) | `Origin` allow-list (§12) |
| `MCP_ALLOW_FIXTURE_PACKS` | `true` in development and test, **must be `false` in production** | Whether packs with `legal_doc: FIXTURE` load |
| `STATS_USER` / `STATS_PASSWORD` | `pm` / — | HTTP Basic for `/internal/stats` (reuses `middleware.BasicAuth`); an empty password leaves the route unmounted |
| `MCP_HOST` · `ACME_EMAIL` | — | Read only by `deploy/compose.yml` on the VPS (Traefik's router and ACME account) |

The production guard of `internal/config` refuses to start `cmd/mcp` when `APP_ENV=production`
and `MCP_ALLOW_FIXTURE_PACKS=true`, `MCP_PUBLIC_URL` is empty, or **`DATABASE_URL` is set** — the
server must not be able to reach a database.

## 3 · Data model — none; the corpus as read

No database, no migration, no DDL. The server reads two read-only trees baked into the image:

- **`/rules`** — the packs (§4), loaded by `rules.Load`.
- **`/legal`** — the corpus produced by `scripts/legal_fetch.py`. The server depends on this shape and `make legal-check` enforces it:

`legal/manifest.yaml` — one entry per document:

```yaml
documents:
  - id: nd-254-2026             # directory name under legal/corpus/
    number: '254/2026/NĐ-CP'    # official number, verbatim; the server prefixes the type (NĐ, TT, Luật…)
    title: '…'
    type: nghi-dinh
    effective_from: 2026-07-01
    effective_to: null
    topics: 'hoa_don'            # §5.3 topic codes, space-separated (the manifest is scalars only)
    replaces: 'NĐ 123/2020/NĐ-CP; NĐ 41/2022/NĐ-CP Điều 1; NĐ 70/2025/NĐ-CP'   # '; '-separated, from the effect article
    status: obtained
    source_url: 'https://…'      # an official portal only
    sha256: '…'                  # of legal/originals/<id>.pdf
    retrieved: 2026-11-22
    note: '…'                    # returned verbatim by van_ban_ap_dung
```

`legal/corpus/<id>/dieu-<n>.md` — one article per file (annexes in `phu-luc.md`); frontmatter `doc` (the
manifest id), `number`, `article` (an integer, or `phu-luc`), `chapter`, `title`, `effective_from`, `effective_to`, `source_url`, `sha256`, `verified`
(default `false`), `verified_by` (null until signed). The body is the article's text verbatim;
khoản start with `<k>.` at the start of a line and điểm with `<letter>)`, as the originals print
them, and the server splits on those markers without rewriting anything.

**Versions.** `corpus_version` = the first 12 hex characters of sha256 over the sorted list of
`<path> <file sha256>` lines of every corpus file and the manifest, computed at load.
`server_version` = the image tag (ldflags). Each pack carries its own `version`. Every result names
all three (§5.0). `van_ban_ap_dung` reads the manifest's `topics` field.

## 4 · Rule-pack kinds

A pack gains three top-level fields; everything else (`legal_doc`, `article`, `title`,
`version`, `regime`, `effective_from`, `effective_to`) is unchanged:

```yaml
kind: posting          # posting (default when absent) | rate | deadline | invoice_fields
verified: false        # default false; true only with verified_by
verified_by: null      # the reviewing accountant's name and the date, e.g. 'Nguyễn … — 2026-12-…'
```

Rules of the loader (`internal/rules`):

- **`kind` defaults to `posting`**, so the two fixture packs and their table load and pass
  unchanged. `Evaluate` considers only `posting` packs.
- A pack carries exactly the list its kind names — `rules` (posting), `rates` and optional
  `thresholds` (rate), `deadlines` and optional `holiday_shift` (deadline), `fields`
  (invoice_fields); any other list is a load error (strict YAML already refuses unknown keys).
- Every row has an `id`, a `citation` in the format of §1 and a `corpus` reference
  `<key>/dieu-<n>` that must exist under `LEGAL_DIR` when the corpus is present (the MCP server
  checks it at start; `make test` checks it when `LEGAL_DIR` resolves).
- `verified: true` without `verified_by` is a load error. A pack whose `corpus` rows point at
  unverified articles may still be verified: the sign-off is on the pack (*Proposed* — the reviewing
  accountant confirms the rule).
- Pack versions stay unique across kinds. For two packs of the same kind and `regime` whose
  effective ranges overlap, their row **keys** (below) must be disjoint — the lookup collects rows
  from every pack in force and a duplicate key would make it ambiguous.
- Money is `int64` VND minor units and rates are **integer basis points** (1% = 100) — no floats
.

### 4.1 · `posting` (exists)

Unchanged (`engine.go`). Key: the pack and the rule
order. Used by `kiem_tra_but_toan`.

### 4.2 · `rate`

```yaml
kind: rate
legal_doc: '<number, pending the corpus>'
article: 'Điều <n>'
title: '…'
version: hkd-rate@<yyyy-mm>
regime: HKD                    # HKD | GTGT
effective_from: <date>
effective_to: null
verified: false
verified_by: null
rates:
  - id: <slug>
    citation: '<doc>, Điều <n>, khoản <k> — effective <date>'
    corpus: '<key>/dieu-<n>'
    tax: gtgt                  # gtgt | tncn
    method: ty_le_doanh_thu    # ty_le_doanh_thu (percentage of revenue) | truc_tiep | khau_tru
    activity_group: <code>     # the group code as the document names it; absent for khau_tru
    activity_label: '…'        # the group's name, verbatim
    rate_bp: <int>             # absent for khau_tru (a formula, not a rate)
thresholds:                    # optional
  - id: <slug>
    citation: '…'
    corpus: '…'
    name: annual_revenue_exempt  # the only name v1 reads
    amount_minor: <int>
```

Key: `(tax, method, activity_group)`. Activity-group codes are **data**: the tools validate an
argument against the codes of the packs in force and list them on `invalid_argument`, so no group
or rate is written in Go.

### 4.3 · `deadline`

```yaml
kind: deadline
regime: QLT
deadlines:
  - id: <slug>
    citation: '…'
    corpus: '…'
    tax: gtgt                  # gtgt | tncn | hkd | …  (codes as in §5.6)
    entity_types: [dn]         # dn | hkd
    method: khau_tru           # optional; absent = any
    period: quarter            # month | quarter | year
    due:
      rule: last_day_of_month  # last_day_of_month | day_of_month
      months_after_period: <int>
      day: <int>               # only for day_of_month
holiday_shift:                 # optional, one per pack
  citation: '…'
  corpus: '…'
  rule: next_working_day       # the only rule v1 implements
  holidays:                    # explicit dates from the official notices, per year
    - {date: <date>, name: '…'}
```

Key: `(tax, entity_type, method, period)`. Saturdays and Sundays are non-working days under
`next_working_day`; public holidays only as listed — the server knows no calendar of its own.

### 4.4 · `invoice_fields`

```yaml
kind: invoice_fields
regime: HDDT
fields:
  - id: <slug>
    citation: '…'
    corpus: '…'
    path: buyer.mst            # a path into the draft of §5.8
    label: '…'                 # the content item's name, verbatim
    required: always           # always | when
    when: {buyer_kind: business}   # only with required: when; keys from §5.8
    check: present             # present | mst | date | positive_int | non_negative_int | enum
    values: []                 # only for check: enum
```

Key: `id`. `mst` checks the format only (10 digits, or 10 + `-` + 3); no look-up of any register.

### 4.6 · Regression tables

`rules/tests/*.yaml` gain an optional top-level `kind` (default `posting`; the fixture table is
unchanged). For `rate`: `request {regime, date, tax, method, activity_group, revenue_minor}` →
`expect {pack_version, row_id, rate_bp, amount_minor}`; for `deadline`: `request {tax,
entity_type, method, period: {kind, year, number}}` → `expect {pack_version, row_id, due_date,
shifted}`; for `invoice_fields`: `request {issue_date, draft}` → `expect {pack_version, missing:
[ids], invalid: [ids]}`. Every real pack has one table, with a case on each side of each effective
boundary. `make test` runs every kind.

## 5 · Tool contract

Every tool is registered with `mcp.AddTool` and a typed input and output, so the SDK publishes
the JSON input schema and the output schema, returns `structuredContent`, and fills the text
content with the JSON of the output. Annotations on every tool: `title` (Vietnamese, below),
`readOnlyHint: true`, `destructiveHint: false`, `idempotentHint: true`, `openWorldHint: false`.
Descriptions are Vietnamese, ≤ 400 characters, and end with *"Chỉ dùng số liệu giả định; không dán
dữ liệu thật của khách hàng."* Common argument types: `date` = string `^\d{4}-\d{2}-\d{2}$`, a
valid calendar date; `amount` = integer VND, `0 ≤ x ≤ 10^15`; `period` = `{kind: month | quarter |
year, year: 2020…2100, number: 1–12 | 1–4 | absent}`; `revenue_by_group` = the array of §5.4.
The schemas below reference these as `#/$defs/date`, `amount`, `period` and `revenue_by_group`.

### 5.0 · The result envelope

```json
{
  "code": "ok",
  "message": "… (Vietnamese, one sentence)",
  "data": { },
  "citations": [
    {
      "id": "nd-254-2026/dieu-11",
      "citation": "NĐ 254/2026/NĐ-CP, Điều 11 — effective 2026-07-01",
      "doc": "NĐ 254/2026/NĐ-CP", "article": "Điều 11", "khoan": null, "diem": null,
      "effective_from": "2026-07-01", "effective_to": null,
      "verified": false, "verified_by": null,
      "source_url": "https://…"
    }
  ],
  "verified": false,
  "illustrative": false,
  "label": null,
  "as_of": "2026-11-22",
  "versions": { "corpus": "3f2a…", "packs": ["…"], "server": "mcp-v0.1.0" },
  "disclaimer": "Thông tin tham khảo … (§7.2)"
}
```

- `verified` is true only when every citation **and** every pack used is verified.
- `illustrative` / `label`: set only by §6's illustrative rule; `label` is the fixed text of §7.2.
- `as_of`: the argument, else today in Asia/Ho_Chi_Minh; for calculators the date the pack was
  selected on (the period's last day, or the transaction / issue date).
- A non-`ok` code still returns the envelope (with `data: null` unless stated); codes marked
  *error* in §6 set `isError: true` on the MCP result so Claude treats them as failures.
- Size: the text and structured content together stay ≤ 100,000 characters (below the client's
  ~150k cap); a longer article is cut at a khoản boundary (§5.2).

### 5.1 · `tra_cuu_van_ban` — *Tìm văn bản pháp luật thuế*

```json
{ "type": "object", "additionalProperties": false, "required": ["query"],
  "properties": {
    "query": { "type": "string", "minLength": 2, "maxLength": 200 },
    "as_of": { "type": "string", "format": "date" },
    "doc":   { "type": "string", "description": "manifest key or official number" },
    "limit": { "type": "integer", "minimum": 1, "maximum": 20, "default": 5 } } }
```

`data.hits[]`: `{id, doc, article, title, snippet, score, effective_from, effective_to,
verified, source_url}`; `citations` = one per hit. Only articles in force on `as_of` are searched
(§10). Codes: `ok`, `no_match`, `invalid_argument`.

### 5.2 · `doc_dieu_khoan` — *Đọc nguyên văn điều khoản*

```json
{ "type": "object", "additionalProperties": false, "required": ["doc", "dieu"],
  "properties": {
    "doc":   { "type": "string" },
    "dieu":  { "type": "integer", "minimum": 1 },
    "khoan": { "type": "integer", "minimum": 1 },
    "diem":  { "type": "string", "pattern": "^[a-zđ]$" },
    "as_of": { "type": "string", "format": "date" } } }
```

`data`: `{id, doc, dieu, khoan, diem, title, text, truncated, next_khoan, sha256}` — `text`
verbatim from the corpus file (the whole article, or the khoản / điểm asked for);
`truncated: true` with `next_khoan` when the article was cut. Codes: `ok`, `no_match` (no such
document, Điều, khoản or điểm), `not_in_force` (*error*: the article exists but `as_of` is outside
its range — `data` holds the range, never the text), `invalid_argument`.

### 5.3 · `van_ban_ap_dung` — *Văn bản áp dụng theo ngày*

```json
{ "type": "object", "additionalProperties": false, "required": ["topic", "as_of"],
  "properties": {
    "topic": { "type": "string", "enum": ["hoa_don", "quan_ly_thue", "gtgt", "hkd", "ke_toan"] },
    "as_of": { "type": "string", "format": "date" } } }
```

`data.documents[]`: `{doc, title, effective_from, effective_to, replaces[], verified,
source_url}` — every manifest document with the topic in force on `as_of`; `data.superseded[]`:
those with the topic whose range ended before `as_of`, so a question around the 2026-07-01 cutover
shows both sides. Codes: `ok`, `no_match`, `invalid_argument`.

### 5.4 · `tinh_thue_hkd` — *Tính thuế hộ kinh doanh*

```json
{ "type": "object", "additionalProperties": false, "required": ["period", "revenue_by_group"],
  "properties": {
    "period": { "$ref": "#/$defs/period" },
    "revenue_by_group": { "type": "array", "minItems": 1, "maxItems": 10,
      "items": { "type": "object", "additionalProperties": false,
        "required": ["activity_group", "revenue_minor"],
        "properties": { "activity_group": { "type": "string" },
                        "revenue_minor": { "$ref": "#/$defs/amount" } } } },
    "illustrative": { "type": "boolean", "default": false } } }
```

Packs: `rate`, regime `HKD`, method `ty_le_doanh_thu`, selected on the period's last day; if the
period's first day selects a different pack version → `period_spans_versions`. `data`:
`{lines[]: {activity_group, activity_label, revenue_minor, gtgt_rate_bp, gtgt_minor,
tncn_rate_bp, tncn_minor, row_ids[]}, totals: {gtgt_minor, tncn_minor}, threshold:
{amount_minor, applies}}`. Arithmetic: `amount = revenue × rate_bp / 10000`, rounded half-up to
the đồng **per line**, then summed — the rounding is an engine convention pending the reviewing
accountant's confirmation, cited as such. `threshold.applies` is computed only for `period.kind =
year` (revenue total ≤ `annual_revenue_exempt`); for shorter periods the threshold is reported
and not applied. Codes: `ok`, `no_match`, `unverified_pack`, `period_spans_versions`,
`invalid_argument` (unknown group — the message lists the groups in force).

### 5.5 · `tinh_thue_gtgt` — *Tính thuế GTGT phải nộp*

```json
{ "type": "object", "additionalProperties": false, "required": ["method", "period"],
  "properties": {
    "method": { "type": "string", "enum": ["khau_tru", "truc_tiep"] },
    "period": { "$ref": "#/$defs/period" },
    "khau_tru": { "type": "object", "additionalProperties": false,
      "required": ["output_vat_minor", "input_vat_deductible_minor"],
      "properties": { "output_vat_minor": { "$ref": "#/$defs/amount" },
                      "input_vat_deductible_minor": { "$ref": "#/$defs/amount" },
                      "carried_forward_minor": { "$ref": "#/$defs/amount" } } },
    "truc_tiep": { "type": "object", "additionalProperties": false,
      "required": ["revenue_by_group"],
      "properties": { "revenue_by_group": { "$ref": "#/$defs/revenue_by_group" } } },
    "illustrative": { "type": "boolean", "default": false } } }
```

Exactly the object of the chosen method must be present (`invalid_argument` otherwise). Packs:
`rate`, regime `GTGT`, the row with `method` = the argument. Deduction: `net = output − input −
carried_forward`; `payable_minor = max(net, 0)`, `carry_forward_minor = max(−net, 0)` — the
method's row supplies the citation. Direct: as §5.4 with the `truc_tiep` rows. `data`: `{method,
payable_minor, carry_forward_minor, lines[]}`. Codes as §5.4.

### 5.6 · `han_nop` — *Tra hạn nộp hồ sơ khai thuế*

```json
{ "type": "object", "additionalProperties": false, "required": ["entity_type", "tax", "period"],
  "properties": {
    "entity_type": { "type": "string", "enum": ["dn", "hkd"] },
    "tax":    { "type": "string", "enum": ["gtgt", "tncn", "hkd"] },
    "method": { "type": "string", "enum": ["khau_tru", "truc_tiep"] },
    "period": { "$ref": "#/$defs/period" },
    "illustrative": { "type": "boolean", "default": false } } }
```

Pack: `deadline`, selected on the period's last day. `data`: `{due_date, unshifted_due_date,
shifted, holiday: {date, name} | null, row_id}`. Codes: `ok`, `no_match`, `unverified_pack`,
`invalid_argument`. A date is a figure: the unverified rule of §6 applies.

### 5.7 · `kiem_tra_but_toan` — *Kiểm tra bút toán nháp*

```json
{ "type": "object", "additionalProperties": false,
  "required": ["regime", "transaction_date", "document", "draft_lines"],
  "properties": {
    "regime": { "type": "string", "enum": ["TT133"] },
    "transaction_date": { "type": "string", "format": "date" },
    "document": { "description": "rules.Document: kind, direction, vat_rate, subtotal_minor, vat_minor, total_minor, counterpart_account" },
    "draft_lines": { "type": "array", "minItems": 1, "maxItems": 50,
      "items": { "type": "object", "additionalProperties": false, "required": ["account_code"],
        "properties": { "account_code": { "type": "string", "pattern": "^[0-9]{3,6}$" },
                        "debit_minor": { "$ref": "#/$defs/amount" },
                        "credit_minor": { "$ref": "#/$defs/amount" } } } },
    "illustrative": { "type": "boolean", "default": false } } }
```

Calls `rules.Evaluate` in process (no HTTP). Lines are compared after summing
by `(account, side)`, order-free. `data`: `{matches, balanced, rule_id, rule_pack_version,
needs_counterpart, expected_lines[], differences[]: {kind: unbalanced | missing | unexpected |
amount_mismatch, account_code, side, expected_minor, draft_minor}}`. Codes: `ok`, `no_match`
(`rules.ErrNoRule`), `unverified_pack`, `invalid_argument` (`rules.IsInvalid`). Fixture packs load
only when `MCP_ALLOW_FIXTURE_PACKS=true`, and a fixture result is always `illustrative`.

### 5.8 · `kiem_tra_hoa_don` — *Kiểm tra hóa đơn điện tử nháp*

```json
{ "type": "object", "additionalProperties": false, "required": ["issue_date", "draft"],
  "properties": {
    "issue_date": { "type": "string", "format": "date" },
    "buyer_kind": { "type": "string", "enum": ["business", "individual"] },
    "draft": { "description": "the canonical invoice of internal/einvoice (Invoice: form, series, number, issued_on, currency, seller, buyer, lines, vat_rates, totals); unknown keys ignored" },
    "illustrative": { "type": "boolean", "default": false } } }
```

Pack: `invoice_fields`, selected on `issue_date`. Each field row's `path` is resolved in the draft
and its `check` applied. `data`: `{checked, passed, missing[]: {field_id, path, label},
invalid[]: {field_id, path, label, reason}}`. Codes: `ok`, `no_match`, `unverified_pack`,
`invalid_argument`. The draft is never stored and never logged.

## 6 · Error codes and the unverified rule

| Code | MCP `isError` | HTTP | Meaning |
|---|---|---|---|
| `ok` | false | 200 | Answered |
| `no_match` | false | 200 | Nothing in the corpus or packs matches; Claude must say so and refer to an accountant |
| `not_in_force` | true | 200 | The text exists but not on `as_of`; range returned, text withheld |
| `unverified_pack` | true | 200 | The pack needed is `verified: false` and `illustrative` was not true; no figure returned |
| `period_spans_versions` | true | 200 | A period straddles two pack versions; `data.boundary` gives the date to split on |
| `invalid_argument` | true | 200 | Schema or semantic validation failed; `message` names the field and the allowed values (whichever layer catches it — the SDK's schema check or the handler — the client sees this code; the tests pin it) |
| `rate_limited` | — | 429 | `middleware.RateLimit`, before the MCP layer; `Retry-After` set; body in the `httpx` error shape |
| `overloaded` | — | 503 | More than `MCP_MAX_INFLIGHT` calls in flight |
| `payload_too_large` | — | 413 | Request body over 256 KiB (`http.MaxBytesReader`) |
| `internal` | true | 200 | A bug; logged with the request id, never with arguments |

**The unverified rule (calculators and checks, §5.4–5.8).** If any pack the answer needs is
`verified: false`: without `illustrative: true` → `unverified_pack`, no figure, no date, no field
list; with it → the answer, `illustrative: true` and the fixed `label`. Lookups (§5.1–5.3) are
never refused: they return the text with its `verified` flag, because a verbatim quote labelled
unverified is not a computed claim.

## 7 · Copy

### 7.1 · Server instructions (Vietnamese, sent on `initialize`)

```text
Máy chủ này cung cấp văn bản pháp luật thuế Việt Nam lấy từ cổng thông tin chính thức và các phép
tính, phép kiểm tra theo bộ quy tắc có phiên bản. Khi dùng máy chủ này:
1. Mọi khẳng định về thuế, hóa đơn hay kế toán phải kèm trích dẫn đúng trường "citation" mà công cụ
   trả về (số văn bản, Điều, khoản, điểm, ngày hiệu lực) và đường dẫn "source_url".
2. Không bao giờ nêu thuế suất, tỷ lệ, ngưỡng doanh thu, số tiền hay hạn nộp nếu không có công cụ
   nào trả về con số đó. Không dùng trí nhớ hay kiến thức chung để thay thế.
3. Khi người dùng hỏi nội dung một điều khoản, trích nguyên văn trường "text"; không diễn giải thay
   cho văn bản.
4. Nếu "verified" là false, nói rõ: "Nội dung chưa được kế toán trưởng đối chiếu với bản gốc."
   Nếu "illustrative" là true, nhắc lại nguyên văn trường "label".
5. Nếu kết quả là "no_match", "not_in_force" hoặc "unverified_pack", hoặc máy chủ không phản hồi,
   nói rằng chưa có quy định áp dụng được xác minh và đề nghị người dùng hỏi kế toán viên. Không
   tự suy đoán câu trả lời.
6. Ngày giao dịch quyết định văn bản áp dụng: luôn truyền "as_of" hoặc ngày giao dịch khi người
   dùng nêu ngày; dùng "van_ban_ap_dung" khi không chắc văn bản nào có hiệu lực.
7. Đây là thông tin tham khảo, không phải dịch vụ kế toán hay tư vấn thuế. Hệ thống không ký hóa
   đơn và không nộp tờ khai thay người dùng.
8. Nhắc người dùng chỉ dùng số liệu giả định, không dán tên, mã số thuế hay dữ liệu thật của
   khách hàng.
```

### 7.2 · Fixed strings

| Key | Text |
|---|---|
| Disclaimer (every result) | *Thông tin tham khảo từ văn bản pháp luật và bộ quy tắc có phiên bản, không phải dịch vụ kế toán hay tư vấn thuế. Hệ thống không ký hóa đơn và không nộp tờ khai thay bạn. Hãy kiểm tra với kế toán viên trước khi kê khai.* |
| Illustrative label | *KẾT QUẢ MINH HỌA — chưa được kế toán trưởng xác minh, không dùng để kê khai.* |
| `no_match` | *Không tìm thấy quy định phù hợp trong kho văn bản. Vui lòng hỏi kế toán viên.* |
| `not_in_force` | *Văn bản này không có hiệu lực vào ngày {as_of} (hiệu lực từ {from} đến {to}).* |
| `unverified_pack` | *Bộ quy tắc này chưa được xác minh nên chưa đưa ra con số. Bạn có thể yêu cầu kết quả minh họa.* |
| `period_spans_versions` | *Kỳ này thuộc hai phiên bản quy định; vui lòng tách kỳ tại ngày {boundary}.* |
| `invalid_argument` | *Thông tin chưa hợp lệ: {field}. {allowed}* |

### 7.3 · Notice page (`GET /`)

One embedded, server-rendered HTML page, `lang="vi"`, no script, no cookies, no third-party
asset. Sections: what it is (the `MCP_PRODUCT_NAME`, the eight tools, the
URL to add); *Thông tin tham khảo* (the disclaimer); *Dữ liệu* — *"Máy chủ không lưu nội dung bạn
gửi. Mỗi lần gọi chỉ ghi tên công cụ, mã kết quả và thời gian xử lý."*; *Claude và dữ liệu của
bạn* — *"Những gì bạn gõ vào Claude được xử lý theo điều khoản gói Claude của bạn. Với gói cá nhân
(Free, Pro, Max), cuộc trò chuyện có thể được dùng để huấn luyện mô hình nếu bạn không tắt tùy chọn
này trong phần cài đặt quyền riêng tư. Đừng dán tên, mã số thuế hay số liệu thật của khách hàng."*;
*Nguồn* — the corpus version, the list of documents with their verification status and source
URLs. Re-check the plan-training wording against Anthropic's current consumer terms before
deploy.

## 8 · Transport and middleware

- **`/mcp`** — `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
  &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})`, mounted for POST, GET and
  DELETE with `gin.WrapH` . Stateless:
  no `Mcp-Session-Id` is kept; GET (server-sent stream) answers 405, as the spec allows.
- **`/healthz`** — `{status, server_version, corpus_version, packs: n, verified_packs: n,
  articles: n}`; 200 when the corpus and packs loaded. The deploy probe compares
  `server_version` with the tag it deployed.
- **`/`** — the notice page. **`/internal/stats`** — §9, behind `middleware.BasicAuth`.
- **Chain** (reused from `internal/middleware`): `RequestID` → `Observe` (with a `nil` perf
  registry: access log only — route, status, latency; the raw path of `/mcp` carries no
  arguments) → `Recovery` → origin check → `RateLimit(nil, "mcp", MCP_RATE_PER_MIN, time.Minute)`
  → in-flight cap → `MaxBytesReader(256 KiB)` → handler. `CORS` is not mounted: the client is
  Anthropic's server, not a browser.
- **Client IP.** gin's trusted proxies = `TRUSTED_PROXIES` (the Traefik network), so `ClientIP`
  is the caller's address. That address is **Anthropic's egress for every Claude user**, so the
  per-IP limit is a coarse abuse guard, set high; per-user limits need auth (later).
- **Timeouts.** `ReadHeaderTimeout` 10 s, a 30 s context deadline per tool call (every tool is
  in-memory and should answer in < 50 ms), `WriteTimeout` 35 s — far inside the client's 240 s.
- **SDK logging.** The SDK's logger is set to discard; only §9's line is written.

## 9 · Telemetry

- **One `slog` JSON line per tool call:** `{msg: "tool_call", request_id, tool, code,
  latency_ms, verified, illustrative, packs: [versions], corpus_version}`. **Never** an argument,
  a result body, a query string, the user agent or the client IP (the access log's IP field is
  dropped for `/mcp` routes).
- **Counters in memory** since start: calls per `(tool, code)`, illustrative count per tool,
  latency p50 / p95 per tool. `GET /internal/stats` returns them as JSON for the operator. A restart resets them, and the log lines are the record (Docker's json-file driver, rotated at
  10 MB × 5).
- No product analytics event, no tracing, no third-party telemetry.

## 10 · Search

- **Index**, built at start from every corpus article: document = title (counted twice) + body.
- **Folding**: Unicode NFD, drop combining marks, `đ`/`Đ` → `d`, lowercase; tokens split on
  anything not a letter or digit. Index **syllable unigrams and bigrams** (Vietnamese words span
  syllables: "giá trị gia tăng").
- **Scoring**: BM25 with `k1 = 1.2`, `b = 0.75` over the folded tokens, plus a 10% bonus per query
  token that also matches unfolded (with its diacritics), so "thuế" outranks "thuê" when typed
  exactly.
- **Filters**: `as_of` (default today, Asia/Ho_Chi_Minh) keeps articles with `effective_from ≤
  as_of ≤ effective_to` (open end = in force); `doc` restricts to one document.
- **Snippet**: the 240-character window with the most query-token hits, cut at word boundaries,
  original text with diacritics.
- No embeddings, no external index, no model.

## 11 · Deployment — one VPS

`deploy/compose.yml` (compose project `vietnam-tax-mcp`):

| Service | Image | Notes |
|---|---|---|
| `traefik` | `TRAEFIK_IMAGE` (a `traefik:v3.x` pin added to `versions.env`) | ports 80 → 443 redirect, 443; ACME TLS-ALPN-01, `ACME_EMAIL`, `acme.json` on a volume; router `Host(${MCP_HOST})` → `mcp:8090`; `headers` (HSTS, `nosniff`, `frame-deny`); `buffering.maxRequestBodyBytes = 262144` |
| `mcp` | `ghcr.io/<owner>/<repo>:${IMAGE_TAG}`, entrypoint `/mcp` | `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, user `nonroot`, memory 256 MB, no published port, no volume, **no `DATABASE_URL`**, healthcheck `/mcp -healthcheck` |

`.github/workflows/deploy.yml`:

1. **Trigger**: push of a tag `v*`, or
   `workflow_dispatch` with `image_tag` (rollback).
2. **Build**: the image from the repository root (`legal/` and `rules/` in context) → trivy
   (CRITICAL, HIGH block) → push `<tag>` and `sha-<short>`.
3. **Deploy** (environment `mcp-production`, protected; secrets `MCP_SSH_HOST`, `MCP_SSH_USER`,
   `MCP_SSH_KEY`, `MCP_HOST`, `ACME_EMAIL`, `STATS_PASSWORD`): rsync `deploy/compose.yml` →
   ssh: copy `deploy.env` to `previous.env`, write the new `IMAGE_TAG` → `docker compose -p
   vietnam-tax-mcp -f mcp.yml --env-file .env --env-file deploy.env pull && up -d` →
   probe `https://$MCP_HOST/healthz` until `server_version` equals the tag (60 s).
4. **Rollback**: if the probe fails, `up -d` with `previous.env` and fail the workflow naming both
   tags; a manual rollback is `workflow_dispatch` with the previous tag. There is no data, so
   there is nothing to migrate or restore.

Content changes only by a deploy: the corpus and packs are in the image, and every answer names
the versions. **Registration**: add `https://<host>/mcp` in Claude Code with
`claude mcp add --transport http <name> https://<host>/mcp`, or in the Claude
app under *Settings → Connectors → Add custom connector*; no OAuth.

## 12 · Security and privacy

- **Stateless and public-law only.** No database, no volume, a read-only filesystem; the
  binary refuses to start in production with `DATABASE_URL` set. It holds no token for the core
  API and no credential of any kind besides the stats password. A draft to check exists in
  memory for the length of one call.
- **Authless v1.** Nothing behind it is private, so OAuth buys nothing yet; the rate limit, the
  in-flight cap and the body limit bound abuse. Any later tool that reads private data needs OAuth first.
- **Rule of Two** (Meta's *Agents Rule of Two*): tool arguments are untrusted input, but the server
  holds no private data and changes no state, so it holds one of the three properties. Arguments
  are data, never instructions; results are JSON and verbatim text, never HTML.
- **Origin check.** A request with an `Origin` header outside `MCP_ALLOWED_ORIGINS` is refused
  403 (the MCP transport's DNS-rebinding guard); a request without `Origin` (server to server)
  passes.
- **Hosting.** The server stores no customer record and runs no trust service;
  it processes public law and transient inputs. What users type into Claude
  is governed by **their** Claude plan, which the notice states, including
  that consumer plans may train on chats.
- **Supply chain.** go-sdk pinned; `govulncheck` and trivy in CI as for the core image.

## 13 · Test plan

| Level | What | Where |
|---|---|---|
| Unit — packs | `kind` defaults to posting and the fixture table passes unchanged; each new kind loads, validates (missing citation, `verified` without `verified_by`, a foreign list, overlapping keys, a `corpus` reference that does not exist) and selects by date; `Evaluate` ignores non-posting packs | `go test ./internal/rules/...` |
| Rule tables | one table per real pack, both sides of each boundary; `make test` covers posting, rate, deadline and invoice_fields | `ci` |
| Unit — tools | per tool: happy path; `no_match`; an unverified pack refused and the same call with `illustrative` labelled; a date before the 2026-07-01 cutover resolving to the pre-cutover text or pack; `invalid_argument` for each schema rule; rounding half-up; carry-forward; holiday shift; the envelope's fields always present | `go test ./internal/mcpserver/...` |
| Unit — search | folded queries find diacritic text; exact diacritics rank higher; `as_of` excludes texts not in force; snippet bounds | same |
| Redaction | a sentinel string in every argument of every tool; the captured `slog` output and the stats JSON never contain it | same |
| Integration — MCP | an SDK client over `httptest`: `initialize` returns the instructions; `tools/list` returns 8 tools, each with a Vietnamese `title` and `readOnlyHint: true`; every call returns a citation and the versions; 429 after the limit; 413 over 256 KiB; 403 for a foreign `Origin`; `/healthz` fields | same, `make test` |
| Manual | MCP Inspector (`npx @modelcontextprotocol/inspector`) against `make run`; Claude Code with the local server answers 5 sample questions with citations | before each release |
| After deploy | the URL added as a Claude custom connector; a fixed question set run and scored by an accountant | before each release |

## 15 · Alternatives considered

- **Python FastMCP.** Rejected: the rule engine is Go, and Python would need a second copy of it.
- **Forking `Ansvar-Systems/vietnamese-law-mcp`.** Rejected: about 93 statutes but no tax
  circulars, no rule-based calculators, no verification status; our provenance (originals, sha256,
  `verified`) would sit on top of a corpus we did not fetch.
- **OAuth now.** Rejected for v1: nothing private to protect, and it adds an authorization server
  to build and run; it comes with the first tool that reads a business's books.
- **A Claude Project with the corpus as files, no server.** Kept as a no-code baseline, not the product: it cannot compute from rule packs or refuse unverified figures.

## 16 · Open items (verified at build 2026-11-23 where stated)

The go-sdk v1.8.0 option names (`Stateless`, `JSONResponse`) and how its schema validation
surfaces an invalid argument; Anthropic's current connector limits and consumer-plan training
wording; the manifest's `topics` field from `scripts/legal_fetch.py`; which VAT law and decree apply on
2026-07-01, and TT 18/2026's amendments; the holiday notices for the years the
deadline pack covers; the rounding convention (reviewing accountant); the subdomain and the product
name.

## 17 · As built (2026-11-23) — where the code differs from or settles §§2–16

- **SDK.** go-sdk v1.8.0 has `StreamableHTTPOptions.Stateless` and `JSONResponse` as named; in
  stateless mode GET and DELETE answer 405. Its schema check runs before the handler and returns
  a plain error result; a receiving middleware (`server.go`, `observe`) turns that into the
  `invalid_argument` envelope and writes the §9 line, so the client sees one code from either
  layer. The input schemas are hand-written JSON (`schemas.go`, the `$defs` spliced in, no
  `$ref`); `format: date` is not enforced by the SDK, so every handler parses dates itself.
- **Corpus contract (§3)** is `scripts/legal_fetch.py`'s: manifest `id` (not `key`), `topics` and `replaces`
  as scalar strings, frontmatter `doc` = id plus `number`; `corpus_version` hashes every `.md`
  under `corpus/` (INDEX.md included) and the manifest. Khoản / điểm are split only when the
  marker continues the sequence and stands outside a “…” quotation (amending text).
- **Pack loader (§4).** A `corpus` reference is required on rate, deadline and invoice_fields
  rows and optional on posting rules (the fixture packs and the existing table load unchanged).
  Regression tables may expect `error: invalid`; the invoice table's request takes `buyer_kind`.
- **Packs written (all `verified: false`)** — `luat-48-2024-qh15/dieu-12.yaml` (direct-method VAT
  b1–b4, from 2025-07-01), `luat-48-2024-qh15/dieu-11.yaml` (deduction formula), `nd-68-2026/dieu-3.yaml`
  (HKD VAT b1–b4 via NĐ 68 Điều 3 khoản 2; threshold 01 tỷ from NĐ 141 Điều 1 khoản 1; from
  2026-01-01 — the engine's reading, to confirm), `nd-252-2026/dieu-10.yaml` (DN month / quarter,
  shift NĐ 252 Điều 3 khoản 7), `nd-68-2026/dieu-8.yaml` (HKD month / quarter / annual PIT; starts
  2026-07-01 with the shift rule), `nd-254-2026/dieu-10.yaml` (19 content items of Điều 10 khoản 1).
  **Not written** (text not in the corpus): HKD PIT rates (Luật 109/2025/QH15 Điều 7), the public
  holiday lists, a real TT 133 posting pack, the Phụ lục exceptions of NĐ 254 Điều 10 khoản 3, and
  every rule before 2026-07-01 that lives in NĐ 123/2020 or NĐ 126/2020.
- **Tool data (§5).** `tinh_thue_hkd`: `tncn_rate_bp` / `tncn_minor` are `null` when no rule is in
  force and `data.unavailable` names the tax; when a year's revenue is within the threshold the
  amounts are 0; for a month or quarter the `message` adds that the threshold is tested on the
  year's revenue, so a sub-year figure is not read as owed by an exempt household. `van_ban_ap_dung` adds `upcoming[]` and each document's `note`, and keeps `data`
  on `no_match` so a date before the cutover shows what takes over. A citation built from a pack
  row carries the row's citation string (with khoản / điểm) and the article's dates and source;
  its `khoan` / `diem` fields stay `null`. `doc_dieu_khoan` cuts an article at 40,000 characters,
  at a khoản boundary.
- **Deployment (§11).** `MCP_HOST` is an environment **variable** (actionlint refuses a secret in
  the environment URL); the workflow writes the VPS `.env` (host, ACME e-mail, stats password, over
  stdin) and `deploy.env` (`IMAGE_REPO`, `IMAGE_TAG`, `TRAEFIK_IMAGE`) and needs
  `MCP_SSH_KNOWN_HOSTS`. `TRAEFIK_IMAGE=traefik:v3.7.16` in `versions.env`. A root `.dockerignore`
  keeps `legal/originals` and `.env*` out of the build context.

