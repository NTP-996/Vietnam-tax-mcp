# Contributing

Thank you for helping. Three kinds of contribution matter most.

## 1. Reviewing law and packs (accountants, lawyers)

Open an issue or a pull request that names the article (`<doc>/dieu-<n>`) or
the pack, what you compared it with, and what you found. A sign-off sets
`verified: true` and `verified_by: "<name> — <date>"` in the file you
reviewed; say whether you compared it with the Công báo original. A pull
request that changes a figure must cite the article that supports it.

## 2. Adding documents to the corpus

Add an entry to `legal/manifest.yaml` with the **official** source URL
(congbao.chinhphu.vn, vbpl.vn, vanban.chinhphu.vn, chinhphu.vn, gdt.gov.vn,
mof.gov.vn). Secondary sites may be used to find a document's number, never as
its text. Run `make legal-fetch` and `make legal-check`, and commit the
original, the corpus files and the manifest together.

## 3. Code

- `make check` must pass (golangci-lint, tests, corpus check). Go code follows
  gofmt/goimports and the Google Go style guide.
- The model never decides tax: a new figure, rate, deadline or rule goes in a
  rule pack with a citation and a `corpus` reference, with a regression table
  in `rules/tests/`, never in Go code or a prompt.
- Tools stay read-only and stateless. Never log an argument or a result.
- Money is integer VND; rates are integer basis points. No floats.
- Update `docs/DESIGN.md` in the same change when behaviour changes.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org).
