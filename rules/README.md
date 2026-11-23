# rules/ — versioned rule packs

Tax figures, deadlines and checks come from these packs through the Go rule
engine (`internal/rules`), never from the model. Each pack is one version of
one article: `legal_doc`, `article`, `title`, `version`, `regime`,
`effective_from` / `effective_to`, `kind`, `verified` / `verified_by`, and the
list its kind names. The engine picks a pack by the transaction, period or
issue date, so texts before and after a cutover resolve by data. The format is
specified in `docs/DESIGN.md` §4.

| Kind | List | Used by |
|---|---|---|
| `posting` (default) | `rules`: a `when` match and the posting `lines` | `kiem_tra_but_toan` |
| `rate` | `rates` in integer basis points, optional `thresholds` | `tinh_thue_hkd`, `tinh_thue_gtgt` |
| `deadline` | `deadlines`, optional `holiday_shift` | `han_nop` |
| `invoice_fields` | `fields`: the required content of an e-invoice | `kiem_tra_hoa_don` |

Every row of the `rate`, `deadline` and `invoice_fields` kinds carries a
`citation` (`<doc>, Điều <n>[, khoản][, điểm] — effective <date>`) and a
`corpus: <doc>/dieu-<n>` reference, which must name a file under
`legal/corpus/`; `make test` checks it. A pack row is written only from text in
the corpus, never from memory or a secondary source.

**Packs from the corpus, all `verified: false`:** `luat-48-2024-qh15/` (VAT
direct-method rates and the deduction formula), `nd-68-2026/` (household
business rates, threshold and deadlines), `nd-252-2026/` (filing deadlines),
`nd-254-2026/` (e-invoice content). Each file's header says what it covers and
what it leaves out because the corpus lacks the text. Until an accountant signs
a pack off (`verified: true` with `verified_by`), the server answers from it
only as a labelled illustrative result.

**`packs/fixture/`** holds two posting packs that cite no law
(`legal_doc: FIXTURE`). They exist for the engine's tests and load only when
`MCP_ALLOW_FIXTURE_PACKS=true`; production refuses them.

`tests/*.yaml` are the expected-result tables, one per pack, with a case on
each side of each effective boundary; `make test` runs them.
