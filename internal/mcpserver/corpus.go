// Package mcpserver is the public, stateless tax-law MCP server of
// docs/DESIGN.md: verbatim articles of the official legal corpus, searched in memory,
// and calculators and checks that are rule-engine output with a citation.
// Nothing about a caller is stored; a tool call's arguments exist in memory
// for the length of the call and never reach a log.
package mcpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Document is one manifest entry (legal/manifest.yaml).
type Document struct {
	ID            string
	Number        string // official number, verbatim (e.g. 254/2026/NĐ-CP)
	Cite          string // the number with its type prefix (NĐ 254/2026/NĐ-CP)
	Title         string
	Type          string
	Topics        []string
	Replaces      []string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	SourceURL     string
	Note          string
	Articles      []*Article
}

// Verified reports whether every article of the document is signed off.
func (d *Document) Verified() bool {
	for _, a := range d.Articles {
		if !a.Verified {
			return false
		}
	}
	return len(d.Articles) > 0
}

// InForce reports effective_from ≤ day ≤ effective_to (open end = in force).
func (d *Document) InForce(day time.Time) bool {
	return inRange(day, d.EffectiveFrom, d.EffectiveTo)
}

func inRange(day, from time.Time, to *time.Time) bool {
	if day.Before(from) {
		return false
	}
	return to == nil || !day.After(*to)
}

// Article is one corpus file (legal/corpus/<doc>/dieu-<n>.md or phu-luc.md).
type Article struct {
	ID            string // <doc>/dieu-<n> or <doc>/phu-luc
	Doc           *Document
	Number        int    // 0 for an annex
	Label         string // "Điều 10" or "Phụ lục"
	Title         string
	Chapter       string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	SourceURL     string
	SHA256        string // the original's hash, as the frontmatter records it
	Verified      bool
	VerifiedBy    string
	Text          string // the article body verbatim, without the heading line
	Khoan         []Khoan
}

// Khoan is one numbered clause; Number 0 is the text before khoản 1.
type Khoan struct {
	Number int
	Text   string
	Diem   []Diem
}

// Diem is one lettered point of a khoản.
type Diem struct {
	Letter string
	Text   string
}

// Corpus is the loaded legal corpus.
type Corpus struct {
	Docs     []*Document
	Articles []*Article
	Version  string // corpus_version
	byID     map[string]*Article
	index    *index
}

// Article returns an article by id.
func (c *Corpus) Article(id string) (*Article, bool) {
	a, ok := c.byID[id]
	return a, ok
}

// HasArticle reports whether a corpus reference (<doc>/dieu-<n>) exists.
func (c *Corpus) HasArticle(ref string) bool {
	_, ok := c.byID[ref]
	return ok
}

// FindLegalDir resolves the corpus root: LEGAL_DIR, else ./legal or the
// repository root's legal/ relative to services/core, else /legal (the image).
func FindLegalDir(configured string) (string, error) {
	for _, c := range []string{configured, "legal", filepath.Join("..", "..", "legal"), "/legal"} {
		if c == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(c, "manifest.yaml")); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("no legal corpus found (set LEGAL_DIR)")
}

type manifestFile struct {
	Documents []struct {
		ID            string `yaml:"id"`
		Number        string `yaml:"number"`
		Title         string `yaml:"title"`
		Type          string `yaml:"type"`
		Status        string `yaml:"status"`
		Topics        string `yaml:"topics"`
		Replaces      string `yaml:"replaces"`
		EffectiveFrom string `yaml:"effective_from"`
		EffectiveTo   string `yaml:"effective_to"`
		SourceURL     string `yaml:"source_url"`
		Note          string `yaml:"note"`
	} `yaml:"documents"`
}

type frontmatter struct {
	Doc           string `yaml:"doc"`
	Number        string `yaml:"number"`
	Article       any    `yaml:"article"`
	Title         string `yaml:"title"`
	Chapter       string `yaml:"chapter"`
	EffectiveFrom string `yaml:"effective_from"`
	EffectiveTo   string `yaml:"effective_to"`
	SourceURL     string `yaml:"source_url"`
	SHA256        string `yaml:"sha256"`
	Verified      bool   `yaml:"verified"`
	VerifiedBy    string `yaml:"verified_by"`
}

// typePrefix is how a citation names a document type (legal-register §5).
var typePrefix = map[string]string{
	"luat": "Luật", "nghi-dinh": "NĐ", "thong-tu": "TT", "nghi-quyet": "NQ", "quyet-dinh": "QĐ", "vbhn": "VBHN",
}

// LoadCorpus reads the manifest and every article under dir/corpus.
func LoadCorpus(dir string) (*Corpus, error) {
	manifestPath := filepath.Join(dir, "manifest.yaml")
	raw, err := os.ReadFile(manifestPath) //nolint:gosec // the image's read-only /legal tree (LEGAL_DIR)
	if err != nil {
		return nil, err
	}
	var m manifestFile
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath, err)
	}
	hashes := []string{"manifest.yaml " + sum(raw)}
	c := &Corpus{byID: map[string]*Article{}}
	docs := map[string]*Document{}
	for _, e := range m.Documents {
		if e.Status != "obtained" {
			continue
		}
		from, err := parseDate(e.EffectiveFrom)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: effective_from: %w", e.ID, err)
		}
		to, err := parseOptDate(e.EffectiveTo)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: effective_to: %w", e.ID, err)
		}
		d := &Document{
			ID: e.ID, Number: e.Number, Title: e.Title, Type: e.Type,
			Topics: strings.Fields(e.Topics), Replaces: splitList(e.Replaces),
			EffectiveFrom: from, EffectiveTo: to, SourceURL: e.SourceURL, Note: e.Note,
		}
		d.Cite = d.Number
		if p := typePrefix[e.Type]; p != "" && !strings.HasPrefix(e.Number, p) {
			d.Cite = p + " " + d.Number
		}
		docs[d.ID] = d
		c.Docs = append(c.Docs, d)
	}
	root := filepath.Join(dir, "corpus")
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		raw, err := os.ReadFile(path) //nolint:gosec // see above
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		hashes = append(hashes, filepath.ToSlash(rel)+" "+sum(raw))
		if filepath.Base(path) == "INDEX.md" {
			return nil
		}
		a, err := parseArticle(raw, docs)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		c.byID[a.ID] = a
		c.Articles = append(c.Articles, a)
		a.Doc.Articles = append(a.Doc.Articles, a)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load corpus from %s: %w", root, err)
	}
	if len(c.Articles) == 0 {
		return nil, fmt.Errorf("no articles under %s", root)
	}
	sort.Slice(c.Articles, func(i, j int) bool { return c.Articles[i].ID < c.Articles[j].ID })
	for _, d := range c.Docs {
		sort.Slice(d.Articles, func(i, j int) bool { return d.Articles[i].Number < d.Articles[j].Number })
	}
	sort.Strings(hashes)
	c.Version = sum([]byte(strings.Join(hashes, "\n")))[:12]
	c.index = buildIndex(c.Articles)
	return c, nil
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ";") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseDate(s string) (time.Time, error) {
	return time.Parse(time.DateOnly, strings.TrimSpace(s))
}

func parseOptDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return nil, nil
	}
	t, err := parseDate(s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

var headingLine = regexp.MustCompile(`^#\s+[^\n]*\n+`)

func parseArticle(raw []byte, docs map[string]*Document) (*Article, error) {
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		return nil, errors.New("no frontmatter")
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return nil, errors.New("unterminated frontmatter")
	}
	var fm frontmatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return nil, err
	}
	doc, ok := docs[fm.Doc]
	if !ok {
		return nil, fmt.Errorf("document %q is not an obtained manifest entry", fm.Doc)
	}
	from, err := parseDate(fm.EffectiveFrom)
	if err != nil {
		return nil, fmt.Errorf("effective_from: %w", err)
	}
	to, err := parseOptDate(fm.EffectiveTo)
	if err != nil {
		return nil, fmt.Errorf("effective_to: %w", err)
	}
	a := &Article{
		Doc: doc, Title: fm.Title, Chapter: fm.Chapter, EffectiveFrom: from, EffectiveTo: to,
		SourceURL: fm.SourceURL, SHA256: fm.SHA256, Verified: fm.Verified && fm.VerifiedBy != "", VerifiedBy: fm.VerifiedBy,
	}
	switch v := fm.Article.(type) {
	case string:
		if v != "phu-luc" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("article %q", v)
			}
			a.Number = n
		}
	case uint64:
		a.Number = int(v) //nolint:gosec // article numbers are small
	case int64:
		a.Number = int(v)
	case int:
		a.Number = v
	default:
		return nil, fmt.Errorf("article %v is neither a number nor phu-luc", fm.Article)
	}
	if a.Number > 0 {
		a.ID = fmt.Sprintf("%s/dieu-%d", doc.ID, a.Number)
		a.Label = fmt.Sprintf("Điều %d", a.Number)
	} else {
		a.ID = doc.ID + "/phu-luc"
		a.Label = "Phụ lục"
	}
	text := strings.TrimSpace(string(body))
	text = strings.TrimSpace(headingLine.ReplaceAllString(text, ""))
	a.Text = text
	a.Khoan = splitKhoan(text)
	return a, nil
}

var (
	khoanMarker = regexp.MustCompile(`^([0-9]{1,3})\.\s`)
	diemMarker  = regexp.MustCompile(`^([a-zđ])\)\s`)
)

// diemOrder is the Vietnamese order of điểm letters (no f, j, w, z).
const diemOrder = "abcdđeghiklmnopqrstuvxy"

// splitKhoan splits an article on its markers without rewriting a character:
// a khoản starts a paragraph with "<k>. " and a điểm with "<letter>) ". A
// marker counts only when it continues the sequence (khoản k+1, the next
// letter) and outside a quotation (“…” — the text an amending document
// inserts), so numbered lines inside a quotation stay in their khoản.
func splitKhoan(text string) []Khoan {
	paras := strings.Split(text, "\n\n")
	out := []Khoan{{Number: 0}}
	depth := 0
	nextDiem := 0
	for _, p := range paras {
		cur := &out[len(out)-1]
		if depth == 0 {
			if m := khoanMarker.FindStringSubmatch(p); m != nil {
				if n, _ := strconv.Atoi(m[1]); n == cur.Number+1 {
					out = append(out, Khoan{Number: n, Text: p})
					nextDiem = 0
					depth += strings.Count(p, "“") - strings.Count(p, "”")
					continue
				}
			}
			if m := diemMarker.FindStringSubmatch(p); m != nil && cur.Number > 0 {
				letters := []rune(diemOrder)
				if nextDiem < len(letters) && m[1] == string(letters[nextDiem]) {
					cur.Diem = append(cur.Diem, Diem{Letter: m[1], Text: p})
					cur.Text += "\n\n" + p
					nextDiem++
					depth += strings.Count(p, "“") - strings.Count(p, "”")
					continue
				}
			}
		}
		if cur.Text == "" {
			cur.Text = p
		} else {
			cur.Text += "\n\n" + p
		}
		if n := len(cur.Diem); n > 0 {
			cur.Diem[n-1].Text += "\n\n" + p
		}
		depth += strings.Count(p, "“") - strings.Count(p, "”")
		if depth < 0 {
			depth = 0
		}
	}
	if out[0].Text == "" {
		out = out[1:]
	}
	return out
}

// FindDoc resolves a manifest id or an official number ("254/2026/NĐ-CP",
// "NĐ 254/2026/NĐ-CP", "nd 254/2026") to a document.
func (c *Corpus) FindDoc(query string) (*Document, bool) {
	q := strings.TrimSpace(query)
	for _, d := range c.Docs {
		if strings.EqualFold(q, d.ID) {
			return d, true
		}
	}
	fq := compact(q)
	if fq == "" {
		return nil, false
	}
	for _, d := range c.Docs {
		num := compact(d.Number)
		if fq == num || fq == compact(d.Cite) {
			return d, true
		}
	}
	// A number given with a type prefix or without the issuer suffix
	// ("nd 254/2026"): its <n>/<year> part must match on digit boundaries.
	for _, d := range c.Docs {
		key := compact(d.Number)
		if m := numberYear.FindString(key); m != "" {
			key = m
		}
		if i := strings.Index(fq, key); i >= 0 && (i == 0 || !isDigit(fq[i-1])) &&
			(i+len(key) == len(fq) || !isDigit(fq[i+len(key)])) {
			return d, true
		}
	}
	return nil, false
}

var numberYear = regexp.MustCompile(`^[0-9]+/[0-9]{4}`)

func compact(s string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(fold(s))
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
