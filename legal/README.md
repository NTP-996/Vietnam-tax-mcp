# Legal corpus

The original texts of the Vietnamese tax, invoice and accounting law the product cites, fetched
from the **official portals only** and split by article. It is the source for the tax-law
server's look-ups (`docs/DESIGN.md`) and for the rule packs' citations.

> **Nothing here is content-verified.** Every corpus file starts `verified: false`,
> `verified_by: null`. Obtaining an original from an official portal is not a legal review: the
> reviewing accountant signs off each article against the original before a rule pack or an answer may
> treat it as verified.

## Layout

| Path | What it is | Written by |
|---|---|---|
| `manifest.yaml` | One entry per document: `id`, `number`, `title`, `type`, `issuer`, `signed`, `effective_from`, `effective_to`, `status`, `format`, `portal_page`, `source_url`, `sha256`, `retrieved`, `note`, `topics` and `replaces` (read by the MCP server, DESIGN.md §3), and `parts` for documents published in several files | by hand |
| `originals/<id>.pdf` · `originals/<id>.part<k>.pdf` · `originals/<id>.html` | The originals, byte for byte as stored (`.gitattributes` marks them binary) | `make legal-fetch` |
| `corpus/<id>/dieu-<n>.md` | One article: YAML frontmatter, then `# Điều n. Title`, then the article text | `make legal-fetch` |
| `corpus/<id>/phu-luc.md` | The annexes (from the first `Phụ lục` after the signature), same frontmatter with `article: phu-luc` | `make legal-fetch` |
| `corpus/INDEX.md` | Documents, article counts, dates, verified counts | `make legal-fetch` |

Document ids are lower-case: `<type>-<number>-<year>[-<issuer>]` — `luat-108-2025-qh15`,
`nd-254-2026`, `tt-91-2026`, `qd-2551-2026-btc`, `nq-198-2025-qh15`, `vbhn-114-2026-vpqh`.

### Corpus frontmatter (the contract the MCP server and the packs read)

```yaml
doc: "nd-254-2026"            # manifest id
number: "254/2026/NĐ-CP"      # official number, verbatim
article: 11                   # int, or "phu-luc"
title: "Trường hợp sử dụng hóa đơn điện tử được miễn phí dịch vụ"   # "" when the article has none
chapter: "Chương II QUY ĐỊNH ĐỐI VỚI HÓA ĐƠN ĐIỆN TỬ"             # "Chương … — Mục …", or null
effective_from: 2026-07-01    # the document's dates (see below)
effective_to: null
source_url: "https://congbaocdn.chinhphu.vn/…"   # the file the article was read from
sha256: "9aa30852…"           # that file's sha256, as in the manifest
verified: false               # true only with verified_by set
verified_by: null
```

Keys appear in exactly this order; values are JSON-quoted strings, ISO dates, integers, `true`,
`false` or `null`. The body is the article text as written — khoản (`1.`), điểm (`a)`, `đ)`,
`c.1)`) and quotations unchanged — with one paragraph per block; only the PDF line wrapping is
undone. Text is Unicode NFC.

## Sources and formats

- **`format: pdf`** — the Công báo (Official Gazette) PDF from `congbaocdn.chinhphu.vn`, which has
  a text layer. The `vanban.chinhphu.vn` copies of the same documents are image-only scans, so they
  are not used as the stored original when the Công báo copy exists.
- **`format: vbpl-html`** — for documents with no Công báo text copy (TT 89/2026/TT-BTC,
  NĐ 359/2025/NĐ-CP), the national legal database of the Bộ Tư pháp, vbpl.vn. Its pages are
  re-rendered with fresh nonces on every request, so the page bytes never hash the same; the
  document itself travels inside the page as one HTML string, which is byte-stable. That string is
  what is stored and hashed (`vbpl_payload` in the script).
- Secondary sites (thuvienphapluat, luatvietnam, KPMG, EY, PwC, MISA…) were used only to learn a
  document's number and date. A document that cannot be found on an official portal is kept in the
  manifest with `status: not_found_official` and a `reason`, and has no original and no corpus.
- **Consolidated text.** `vbhn-114-2026-vpqh` is the Văn phòng Quốc hội consolidation of the VAT
  law (48/2024/QH15 as amended by 90/2025/QH15, 149/2025/QH15 and 09/2026/QH16). It is a reading
  aid, not a source of law: its footnotes land inside articles where they fall on the page, and a
  rule cites the amending law, not the consolidation.

## Dates

`effective_from` / `effective_to` are the **document's** dates as the portal and the document's
own effect article state them. Some articles take effect on another date; those exceptions are
written in the manifest `note` (for example Luật 108/2025/QH15 Điều 52: Điều 13 and the HKD
e-invoice rule of Điều 26 from 2026-01-01) and are not yet parsed per article. An amended document
stays in force (`effective_to: null`); the amending document has its own entry. A consumer that
answers "which text applies on date X" must read the amending documents too.

## Commands

```sh
make legal-fetch    # python3 -I scripts/legal_fetch.py — download, check sha256, rebuild the corpus
make legal-check    # python3 -I scripts/legal_fetch.py --check — no network, no pdftotext; in docs-check and ci-docs
python3 -I scripts/legal_fetch.py --offline [--doc nd-254-2026]   # rebuild from the stored originals
```

`legal-fetch` needs the network and `pdftotext` (poppler-utils). Every download lands in a fresh
temporary directory and is only hashed, copied and parsed; nothing in it is executed, and the script
runs with `python3 -I`. A download whose sha256 differs from the manifest is an error: nothing is
overwritten and the new copy is left in its temporary directory for review. Accepting a new version
of an original is a manifest edit (new `sha256`, `retrieved`) and a register change.

**Adding a document.** Find it on an official portal; add its entry with `sha256` set to 64 zeros;
run `python3 -I scripts/legal_fetch.py --doc <id>`, which fails and prints the real sha256 and the
temporary directory holding the download; open that copy, confirm it is the document, put the hash
in the manifest and run the command again. Then add or update its row in the legal register.

`legal-check` fails on: a manifest field missing or malformed; a URL that is not HTTPS on an
official portal; an original missing, unlisted or with a different sha256; a corpus file whose
frontmatter keys, `doc`, `number`, `article`, dates or `source_url`/`sha256` disagree with the
manifest; `verified: true` without `verified_by`; text that is not NFC; article numbers that are not
1…n; a stale `INDEX.md`.

## Sign-off

The reviewing accountant sets `verified: true` and `verified_by: "<name>, <YYYY-MM-DD>"` on each
article after comparing it with the original. A rebuild keeps that sign-off while the article text
is unchanged; when a rebuild changes the text it drops the sign-off and prints a warning.

## Known extraction limits

- Article titles that wrap onto a line starting with a capital stay partly in the body (for
  example QĐ 2551/QĐ-BTC Điều 1); the words are all there, in order.
- Tables in PDFs come out one cell or one line per paragraph; the vbpl.vn tables come out one row
  per line, cells separated by ` | `. Rate tables used by a rule pack are read against the original.
- Annexes (forms, the TT 133 chart of accounts) are kept as one `phu-luc.md` per document, without
  further structure.
