package mcpserver

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// BM25 parameters.
const (
	bm25K1      = 1.2
	bm25B       = 0.75
	exactBonus  = 0.10 // per query syllable that also matches with its diacritics
	snippetSize = 240  // runes
)

// foldRune maps one rune to its base letter, lower-cased: NFD, drop the
// combining marks, đ/Đ → d. One rune in, one rune out, so positions in the
// folded text line up with the original.
func foldRune(r rune) rune {
	switch r {
	case 'đ', 'Đ':
		return 'd'
	}
	if r < 0x80 {
		return unicode.ToLower(r)
	}
	for _, d := range norm.NFD.String(string(r)) {
		if !unicode.Is(unicode.Mn, d) {
			return unicode.ToLower(d)
		}
	}
	return unicode.ToLower(r)
}

// fold folds a whole string (search keys, document numbers).
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(s) {
		b.WriteRune(foldRune(r))
	}
	return b.String()
}

// token is one syllable with its rune span in the source text.
type token struct {
	folded, exact string
	start, end    int // rune offsets
}

// tokenize splits on anything that is not a letter or a digit.
func tokenize(s string) []token {
	runes := []rune(norm.NFC.String(s))
	var out []token
	start := -1
	for i := 0; i <= len(runes); i++ {
		inWord := i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || unicode.Is(unicode.Mn, runes[i]))
		switch {
		case inWord && start < 0:
			start = i
		case !inWord && start >= 0:
			word := runes[start:i]
			f := make([]rune, 0, len(word))
			for _, r := range word {
				if !unicode.Is(unicode.Mn, r) {
					f = append(f, foldRune(r))
				}
			}
			out = append(out, token{folded: string(f), exact: strings.ToLower(string(word)), start: start, end: i})
			start = -1
		}
	}
	return out
}

// terms are the indexed keys: folded syllables and folded syllable bigrams.
func terms(toks []token) []string {
	out := make([]string, 0, 2*len(toks))
	for i, t := range toks {
		out = append(out, t.folded)
		if i+1 < len(toks) {
			out = append(out, t.folded+" "+toks[i+1].folded)
		}
	}
	return out
}

type posting struct {
	tf    map[string]int
	exact map[string]bool
	len   int
}

type index struct {
	docs   []*Article
	post   []posting
	df     map[string]int
	avgLen float64
}

func buildIndex(articles []*Article) *index {
	ix := &index{docs: articles, df: map[string]int{}}
	total := 0
	for _, a := range articles {
		// The title counts twice.
		toks := tokenize(a.Title + "\n" + a.Title + "\n" + a.Text)
		ts := terms(toks)
		p := posting{tf: map[string]int{}, exact: map[string]bool{}, len: len(ts)}
		for _, t := range ts {
			p.tf[t]++
		}
		for _, t := range toks {
			p.exact[t.exact] = true
		}
		for t := range p.tf {
			ix.df[t]++
		}
		total += len(ts)
		ix.post = append(ix.post, p)
	}
	if len(articles) > 0 {
		ix.avgLen = float64(total) / float64(len(articles))
	}
	return ix
}

// Hit is one search result.
type Hit struct {
	Article *Article
	Score   float64
	Snippet string
}

// Search ranks the articles in force on asOf (and of one document when doc is
// non-nil) against the query.
func (c *Corpus) Search(query string, asOf time.Time, doc *Document, limit int) []Hit {
	ix := c.index
	qtoks := tokenize(query)
	qterms := dedupe(terms(qtoks))
	if len(qterms) == 0 {
		return nil
	}
	n := float64(len(ix.docs))
	var hits []Hit
	for i, a := range ix.docs {
		if doc != nil && a.Doc != doc {
			continue
		}
		if !inRange(asOf, a.EffectiveFrom, a.EffectiveTo) {
			continue
		}
		p := ix.post[i]
		score := 0.0
		for _, t := range qterms {
			tf := float64(p.tf[t])
			if tf == 0 {
				continue
			}
			df := float64(ix.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(p.len)/ix.avgLen))
		}
		if score == 0 {
			continue
		}
		exact := 0
		for _, t := range qtoks {
			if t.exact != t.folded && p.exact[t.exact] {
				exact++
			}
		}
		score *= 1 + exactBonus*float64(exact)
		hits = append(hits, Hit{Article: a, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Article.ID < hits[j].Article.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	want := map[string]bool{}
	for _, t := range qtoks {
		want[t.folded] = true
	}
	for i := range hits {
		hits[i].Snippet = snippet(hits[i].Article.Text, want)
	}
	return hits
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// snippet is the window of at most snippetSize runes with the most query
// syllables, cut at word boundaries, in the original text.
func snippet(text string, want map[string]bool) string {
	runes := []rune(norm.NFC.String(text))
	toks := tokenize(text)
	if len(toks) == 0 {
		return ""
	}
	bestStart, bestHits := 0, -1
	for i, t := range toks {
		if !want[t.folded] && i != 0 {
			continue
		}
		hits := 0
		for j := i; j < len(toks) && toks[j].end-t.start <= snippetSize; j++ {
			if want[toks[j].folded] {
				hits++
			}
		}
		if hits > bestHits {
			bestStart, bestHits = i, hits
		}
	}
	start := toks[bestStart].start
	end := start
	for j := bestStart; j < len(toks) && toks[j].end-start <= snippetSize; j++ {
		end = toks[j].end
	}
	out := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
