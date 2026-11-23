// Package rules is the deterministic rule engine: tax
// and posting treatment comes from versioned rule packs, never from the model.
// Packs are YAML files under rules/packs/<legal-document>/<article>.yaml, each
// with a legal citation, a version and an effective range; a pack is chosen by
// the transaction date, so documents before and after a cutover resolve by
// data. Every answer records the pack version it used.
package rules

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrNoRule is returned when no pack in force on the transaction date has a
// rule for the document (code rule_not_found).
var ErrNoRule = errors.New("rule_not_found")

// DateLayout is the date format of requests and packs.
const DateLayout = "2006-01-02"

// CounterpartPlaceholder marks the account the caller supplies.
const CounterpartPlaceholder = "{counterpart}"

// Document kinds and directions a rule can match.
const (
	KindInvoice     = "invoice"
	DirectionInput  = "input"
	DirectionOutput = "output"
)

// Pack is one article version. Kind selects which list it carries (DESIGN.md
// §4): posting packs hold Rules, rate packs Rates and Thresholds, deadline
// packs Deadlines and a HolidayShift, invoice_fields packs Fields.
type Pack struct {
	Kind          string     `yaml:"kind" json:"kind"`
	LegalDoc      string     `yaml:"legal_doc" json:"legal_doc"`
	Article       string     `yaml:"article" json:"article"`
	Title         string     `yaml:"title" json:"title"`
	Version       string     `yaml:"version" json:"version"`
	Regime        string     `yaml:"regime" json:"regime"`
	EffectiveFrom civilDate  `yaml:"effective_from" json:"effective_from"`
	EffectiveTo   *civilDate `yaml:"effective_to" json:"effective_to"`
	// Verified is the reviewing accountant's sign-off on the pack;
	// true only with VerifiedBy.
	Verified   bool   `yaml:"verified" json:"verified"`
	VerifiedBy string `yaml:"verified_by" json:"verified_by"`

	Rules        []Rule        `yaml:"rules" json:"rules,omitempty"`
	Rates        []RateRow     `yaml:"rates" json:"rates,omitempty"`
	Thresholds   []Threshold   `yaml:"thresholds" json:"thresholds,omitempty"`
	Deadlines    []DeadlineRow `yaml:"deadlines" json:"deadlines,omitempty"`
	HolidayShift *HolidayShift `yaml:"holiday_shift" json:"holiday_shift,omitempty"`
	Fields       []FieldRow    `yaml:"fields" json:"fields,omitempty"`

	Checksum string `yaml:"-" json:"checksum"` // sha256 of the file, hex
	Path     string `yaml:"-" json:"path"`
}

// Rule is one treatment inside a pack.
type Rule struct {
	ID                 string         `yaml:"id" json:"id"`
	Citation           string         `yaml:"citation" json:"citation"`
	Corpus             string         `yaml:"corpus" json:"corpus,omitempty"`
	When               Match          `yaml:"when" json:"when"`
	CounterpartDefault string         `yaml:"counterpart_default" json:"counterpart_default"`
	Lines              []LineTemplate `yaml:"lines" json:"lines"`
}

// Match selects the documents a rule applies to; an empty field matches all.
type Match struct {
	Document  string   `yaml:"document" json:"document"`
	Direction string   `yaml:"direction" json:"direction"`
	VATRates  []string `yaml:"vat_rates" json:"vat_rates"`
}

// LineTemplate is one posting line: an account (or the counterpart
// placeholder), a side and the document total it carries.
type LineTemplate struct {
	Account string `yaml:"account" json:"account"`
	Side    string `yaml:"side" json:"side"`     // debit | credit
	Amount  string `yaml:"amount" json:"amount"` // subtotal | vat | total
}

// Request is the evaluation input.
type Request struct {
	Regime          string   `json:"regime"`
	TransactionDate string   `json:"transaction_date"`
	Document        Document `json:"document"`
}

// Document is the part of a canonical document the rules look at. Money is
// VND minor units; the VAT rate is a string so "10", "10%", "KCT"
// and "KKKNT" all have a home.
type Document struct {
	Kind               string `json:"kind"`
	Direction          string `json:"direction"`
	VATRate            string `json:"vat_rate"`
	SubtotalMinor      int64  `json:"subtotal_minor"`
	VATMinor           int64  `json:"vat_minor"`
	TotalMinor         int64  `json:"total_minor"`
	CounterpartAccount string `json:"counterpart_account,omitempty"`
}

// Response is the treatment: the pack version the answer records, the
// citation it shows, and the posting lines. NeedsCounterpart says the
// placeholder was filled with the rule's default because the request named
// no counterpart account.
type Response struct {
	RulePackVersion    string `json:"rule_pack_version"`
	RuleID             string `json:"rule_id"`
	LegalDoc           string `json:"legal_doc"`
	Article            string `json:"article"`
	Citation           string `json:"citation"`
	EffectiveFrom      string `json:"effective_from"`
	NeedsCounterpart   bool   `json:"needs_counterpart"`
	CounterpartDefault string `json:"counterpart_default,omitempty"`
	Lines              []Line `json:"lines"`
}

// Line is one posting line of the response (the ledger's shape).
type Line struct {
	AccountCode string `json:"account_code"`
	DebitMinor  int64  `json:"debit_minor"`
	CreditMinor int64  `json:"credit_minor"`
}

// Engine holds the loaded packs, newest effective date first.
type Engine struct {
	packs []Pack
}

// New builds an engine from validated packs.
func New(packs []Pack) (*Engine, error) {
	packs = append([]Pack(nil), packs...)
	for i := range packs {
		if packs[i].Kind == "" {
			packs[i].Kind = PackKindPosting
		}
		if err := packs[i].validate(); err != nil {
			return nil, fmt.Errorf("pack %s: %w", packs[i].Path, err)
		}
	}
	versions := map[string]string{}
	for _, p := range packs {
		if other, dup := versions[p.Version]; dup {
			return nil, fmt.Errorf("pack version %q appears in %s and %s", p.Version, other, p.Path)
		}
		versions[p.Version] = p.Path
	}
	if err := checkOverlaps(packs); err != nil {
		return nil, err
	}
	sorted := make([]Pack, len(packs))
	copy(sorted, packs)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].EffectiveFrom.After(sorted[j].EffectiveFrom.Time)
	})
	return &Engine{packs: sorted}, nil
}

// Packs lists the loaded packs (for the registry and the health page).
func (e *Engine) Packs() []Pack { return e.packs }

// Evaluate chooses the pack in force on the transaction date for the regime
// and the first rule that matches the document.
func (e *Engine) Evaluate(req Request) (*Response, error) {
	date, err := time.Parse(DateLayout, strings.TrimSpace(req.TransactionDate))
	if err != nil {
		return nil, fmt.Errorf("transaction_date must be YYYY-MM-DD: %w", errInvalid)
	}
	regime := strings.ToUpper(strings.TrimSpace(req.Regime))
	if regime == "" {
		return nil, fmt.Errorf("regime is required: %w", errInvalid)
	}
	doc := req.Document
	doc.Kind = strings.ToLower(strings.TrimSpace(doc.Kind))
	doc.Direction = strings.ToLower(strings.TrimSpace(doc.Direction))
	doc.VATRate = normalizeRate(doc.VATRate)
	doc.CounterpartAccount = strings.TrimSpace(doc.CounterpartAccount)

	for _, pack := range e.packs {
		if pack.Kind != PackKindPosting || pack.Regime != regime || !pack.inForce(date) {
			continue
		}
		for _, rule := range pack.Rules {
			if !rule.When.matches(doc) {
				continue
			}
			return render(pack, rule, doc)
		}
	}
	return nil, ErrNoRule
}

var errInvalid = errors.New("invalid request")

// IsInvalid reports whether err is a malformed request rather than a miss.
func IsInvalid(err error) bool { return errors.Is(err, errInvalid) }

func (p Pack) inForce(date time.Time) bool {
	if date.Before(p.EffectiveFrom.Time) {
		return false
	}
	if p.EffectiveTo != nil && date.After(p.EffectiveTo.Time) {
		return false
	}
	return true
}

func (m Match) matches(doc Document) bool {
	if m.Document != "" && m.Document != doc.Kind {
		return false
	}
	if m.Direction != "" && m.Direction != doc.Direction {
		return false
	}
	if len(m.VATRates) > 0 {
		found := false
		for _, r := range m.VATRates {
			if normalizeRate(r) == doc.VATRate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func render(pack Pack, rule Rule, doc Document) (*Response, error) {
	resp := &Response{
		RulePackVersion: pack.Version,
		RuleID:          rule.ID,
		LegalDoc:        pack.LegalDoc,
		Article:         pack.Article,
		Citation:        rule.Citation,
		EffectiveFrom:   pack.EffectiveFrom.Format(DateLayout),
		Lines:           make([]Line, 0, len(rule.Lines)),
	}
	for _, tmpl := range rule.Lines {
		account := tmpl.Account
		if account == CounterpartPlaceholder {
			switch {
			case doc.CounterpartAccount != "":
				account = doc.CounterpartAccount
			case rule.CounterpartDefault != "":
				account = rule.CounterpartDefault
				resp.NeedsCounterpart = true
				resp.CounterpartDefault = rule.CounterpartDefault
			default:
				return nil, fmt.Errorf("rule %s needs a counterpart account and has no default: %w", rule.ID, errInvalid)
			}
		}
		amount, err := doc.amount(tmpl.Amount)
		if err != nil {
			return nil, err
		}
		line := Line{AccountCode: account}
		if tmpl.Side == "debit" {
			line.DebitMinor = amount
		} else {
			line.CreditMinor = amount
		}
		resp.Lines = append(resp.Lines, line)
	}
	return resp, nil
}

func (d Document) amount(name string) (int64, error) {
	switch name {
	case "subtotal":
		return d.SubtotalMinor, nil
	case "vat":
		return d.VATMinor, nil
	case "total":
		return d.TotalMinor, nil
	}
	return 0, fmt.Errorf("unknown amount %q in pack", name)
}

// normalizeRate turns "10%", " 10 " and "10" into "10"; words (KCT, KKKNT) are
// upper-cased.
func normalizeRate(rate string) string {
	rate = strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rate), "%")))
	return rate
}

func (p Pack) validate() error {
	switch {
	case p.LegalDoc == "", p.Article == "", p.Version == "", p.Regime == "":
		return errors.New("legal_doc, article, version and regime are required")
	case p.EffectiveFrom.IsZero():
		return errors.New("effective_from is required")
	case p.EffectiveTo != nil && p.EffectiveTo.Before(p.EffectiveFrom.Time):
		return errors.New("effective_to is before effective_from")
	case p.Verified && strings.TrimSpace(p.VerifiedBy) == "":
		return errors.New("verified: true needs verified_by (the reviewing accountant's sign-off)")
	}
	if err := p.validateLists(); err != nil {
		return err
	}
	switch p.Kind {
	case PackKindRate:
		return p.validateRates()
	case PackKindDeadline:
		return p.validateDeadlines()
	case PackKindInvoiceFields:
		return p.validateFields()
	}
	return p.validatePosting()
}

func (p Pack) validatePosting() error {
	if len(p.Rules) == 0 {
		return errors.New("a pack needs at least one rule")
	}
	for _, r := range p.Rules {
		if err := p.checkRow(r.ID, r.Citation, r.Corpus); err != nil {
			return err
		}
		if len(r.Lines) < 2 {
			return fmt.Errorf("rule %s needs at least two lines", r.ID)
		}
		for _, l := range r.Lines {
			if l.Account == "" {
				return fmt.Errorf("rule %s: a line needs an account", r.ID)
			}
			if l.Side != "debit" && l.Side != "credit" {
				return fmt.Errorf("rule %s: side must be debit or credit", r.ID)
			}
			if l.Amount != "subtotal" && l.Amount != "vat" && l.Amount != "total" {
				return fmt.Errorf("rule %s: amount must be subtotal, vat or total", r.ID)
			}
		}
	}
	return nil
}

// civilDate is a YYYY-MM-DD value in YAML and JSON.
type civilDate struct {
	time.Time
}

func (d *civilDate) UnmarshalYAML(unmarshal func(any) error) error {
	var raw string
	if err := unmarshal(&raw); err != nil {
		return err
	}
	return d.parse(raw)
}

func (d *civilDate) UnmarshalText(text []byte) error { return d.parse(string(text)) }

func (d *civilDate) parse(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	t, err := time.Parse(DateLayout, raw)
	if err != nil {
		return fmt.Errorf("date %q is not YYYY-MM-DD", raw)
	}
	d.Time = t
	return nil
}

func (d civilDate) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + d.Format(DateLayout) + `"`), nil
}
