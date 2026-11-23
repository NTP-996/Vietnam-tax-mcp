package rules

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Pack kinds. A pack without a kind is a posting pack, so the
// packs written before kinds existed load unchanged.
const (
	PackKindPosting       = "posting"
	PackKindRate          = "rate"
	PackKindDeadline      = "deadline"
	PackKindInvoiceFields = "invoice_fields"
)

// FixtureDoc is the legal_doc of the fixture packs, which cite no law and
// therefore carry no corpus reference.
const FixtureDoc = "FIXTURE"

// Rate methods: a percentage of revenue (HKD), the direct method on revenue
// (VAT) and the deduction method (VAT; a formula, so its row has no rate).
const (
	MethodRevenueShare = "ty_le_doanh_thu"
	MethodDirect       = "truc_tiep"
	MethodDeduction    = "khau_tru"
)

// ThresholdAnnualRevenueExempt is the only threshold name v1 reads.
const ThresholdAnnualRevenueExempt = "annual_revenue_exempt"

// Period kinds.
const (
	PeriodMonth   = "month"
	PeriodQuarter = "quarter"
	PeriodYear    = "year"
)

// RateRow is one rate of a rate pack: integer basis points (1% = 100), never
// a float.
type RateRow struct {
	ID            string `yaml:"id" json:"id"`
	Citation      string `yaml:"citation" json:"citation"`
	Corpus        string `yaml:"corpus" json:"corpus"`
	Tax           string `yaml:"tax" json:"tax"`
	Method        string `yaml:"method" json:"method"`
	ActivityGroup string `yaml:"activity_group" json:"activity_group,omitempty"`
	ActivityLabel string `yaml:"activity_label" json:"activity_label,omitempty"`
	RateBP        *int64 `yaml:"rate_bp" json:"rate_bp,omitempty"`
}

// Threshold is a named amount of a rate pack (VND).
type Threshold struct {
	ID          string `yaml:"id" json:"id"`
	Citation    string `yaml:"citation" json:"citation"`
	Corpus      string `yaml:"corpus" json:"corpus"`
	Name        string `yaml:"name" json:"name"`
	AmountMinor int64  `yaml:"amount_minor" json:"amount_minor"`
}

// DeadlineRow maps a filing period to its due date.
type DeadlineRow struct {
	ID          string   `yaml:"id" json:"id"`
	Citation    string   `yaml:"citation" json:"citation"`
	Corpus      string   `yaml:"corpus" json:"corpus"`
	Tax         string   `yaml:"tax" json:"tax"`
	EntityTypes []string `yaml:"entity_types" json:"entity_types"`
	Method      string   `yaml:"method" json:"method,omitempty"` // empty = any method
	Period      string   `yaml:"period" json:"period"`
	Due         DueRule  `yaml:"due" json:"due"`
}

// DueRule is the due date relative to the period's last month.
type DueRule struct {
	Rule              string `yaml:"rule" json:"rule"` // last_day_of_month | day_of_month
	MonthsAfterPeriod int    `yaml:"months_after_period" json:"months_after_period"`
	Day               int    `yaml:"day" json:"day,omitempty"`
}

// HolidayShift moves a due date that falls on a non-working day. Saturdays and
// Sundays are non-working; public holidays only as listed — the engine knows
// no calendar of its own.
type HolidayShift struct {
	Citation string    `yaml:"citation" json:"citation"`
	Corpus   string    `yaml:"corpus" json:"corpus"`
	Rule     string    `yaml:"rule" json:"rule"` // next_working_day
	Holidays []Holiday `yaml:"holidays" json:"holidays"`
}

// Holiday is one listed non-working day.
type Holiday struct {
	Date civilDate `yaml:"date" json:"date"`
	Name string    `yaml:"name" json:"name"`
}

// FieldRow is one required content item of an e-invoice.
type FieldRow struct {
	ID       string            `yaml:"id" json:"id"`
	Citation string            `yaml:"citation" json:"citation"`
	Corpus   string            `yaml:"corpus" json:"corpus"`
	Path     string            `yaml:"path" json:"path"`
	Label    string            `yaml:"label" json:"label"`
	Required string            `yaml:"required" json:"required"` // always | when
	When     map[string]string `yaml:"when" json:"when,omitempty"`
	Check    string            `yaml:"check" json:"check"`
	Values   []string          `yaml:"values" json:"values,omitempty"`
}

var (
	corpusRef   = regexp.MustCompile(`^[a-z0-9-]+/(dieu-[0-9]+|phu-luc)$`)
	mstPattern  = regexp.MustCompile(`^[0-9]{10}(-[0-9]{3})?$`)
	fieldChecks = []string{"present", "mst", "date", "positive_int", "non_negative_int", "enum"}
	whenKeys    = []string{"buyer_kind"}
)

// validateLists enforces "a pack carries exactly the list its kind names".
func (p Pack) validateLists() error {
	has := map[string]bool{
		"rules":         len(p.Rules) > 0,
		"rates":         len(p.Rates) > 0,
		"thresholds":    len(p.Thresholds) > 0,
		"deadlines":     len(p.Deadlines) > 0,
		"holiday_shift": p.HolidayShift != nil,
		"fields":        len(p.Fields) > 0,
	}
	allowed := map[string][]string{
		PackKindPosting:       {"rules"},
		PackKindRate:          {"rates", "thresholds"},
		PackKindDeadline:      {"deadlines", "holiday_shift"},
		PackKindInvoiceFields: {"fields"},
	}
	lists, ok := allowed[p.Kind]
	if !ok {
		return fmt.Errorf("kind %q is not posting, rate, deadline or invoice_fields", p.Kind)
	}
	for name, present := range has {
		if present && !slices.Contains(lists, name) {
			return fmt.Errorf("a %s pack cannot carry %s", p.Kind, name)
		}
	}
	return nil
}

// checkRow is the rule every row shares: an id, a citation and a corpus
// reference <doc>/dieu-<n>. Posting rules (which predate the corpus) and the
// fixture packs may omit the reference; when given it must be well formed.
func (p Pack) checkRow(id, citation, corpus string) error {
	if id == "" || citation == "" {
		return fmt.Errorf("row %q needs an id and a citation", id)
	}
	if corpus == "" && (p.LegalDoc == FixtureDoc || p.Kind == PackKindPosting) {
		return nil
	}
	if !corpusRef.MatchString(corpus) {
		return fmt.Errorf("row %s: corpus %q is not <doc>/dieu-<n>", id, corpus)
	}
	return nil
}

func (p Pack) validateRates() error {
	if len(p.Rates) == 0 {
		return errors.New("a rate pack needs at least one rate")
	}
	ids := map[string]bool{}
	for _, r := range p.Rates {
		if err := p.checkRow(r.ID, r.Citation, r.Corpus); err != nil {
			return err
		}
		if ids[r.ID] {
			return fmt.Errorf("row id %s repeats", r.ID)
		}
		ids[r.ID] = true
		if r.Tax != "gtgt" && r.Tax != "tncn" {
			return fmt.Errorf("rate %s: tax must be gtgt or tncn", r.ID)
		}
		switch r.Method {
		case MethodDeduction:
			if r.RateBP != nil || r.ActivityGroup != "" {
				return fmt.Errorf("rate %s: a khau_tru row has no rate_bp and no activity_group", r.ID)
			}
		case MethodRevenueShare, MethodDirect:
			if r.RateBP == nil || *r.RateBP < 0 || *r.RateBP > 10000 {
				return fmt.Errorf("rate %s: rate_bp must be 0..10000 basis points", r.ID)
			}
			if r.ActivityGroup == "" || r.ActivityLabel == "" {
				return fmt.Errorf("rate %s: activity_group and activity_label are required", r.ID)
			}
		default:
			return fmt.Errorf("rate %s: method must be ty_le_doanh_thu, truc_tiep or khau_tru", r.ID)
		}
	}
	for _, t := range p.Thresholds {
		if err := p.checkRow(t.ID, t.Citation, t.Corpus); err != nil {
			return err
		}
		if t.Name != ThresholdAnnualRevenueExempt || t.AmountMinor < 0 {
			return fmt.Errorf("threshold %s: name must be %s with a non-negative amount", t.ID, ThresholdAnnualRevenueExempt)
		}
	}
	return nil
}

func (p Pack) validateDeadlines() error {
	if len(p.Deadlines) == 0 {
		return errors.New("a deadline pack needs at least one deadline")
	}
	ids := map[string]bool{}
	for _, d := range p.Deadlines {
		if err := p.checkRow(d.ID, d.Citation, d.Corpus); err != nil {
			return err
		}
		if ids[d.ID] {
			return fmt.Errorf("row id %s repeats", d.ID)
		}
		ids[d.ID] = true
		if d.Tax == "" || len(d.EntityTypes) == 0 {
			return fmt.Errorf("deadline %s: tax and entity_types are required", d.ID)
		}
		for _, et := range d.EntityTypes {
			if et != "dn" && et != "hkd" {
				return fmt.Errorf("deadline %s: entity type %q is not dn or hkd", d.ID, et)
			}
		}
		if d.Period != PeriodMonth && d.Period != PeriodQuarter && d.Period != PeriodYear {
			return fmt.Errorf("deadline %s: period must be month, quarter or year", d.ID)
		}
		if d.Due.MonthsAfterPeriod < 0 || d.Due.MonthsAfterPeriod > 24 {
			return fmt.Errorf("deadline %s: months_after_period must be 0..24", d.ID)
		}
		switch d.Due.Rule {
		case "last_day_of_month":
			if d.Due.Day != 0 {
				return fmt.Errorf("deadline %s: day only goes with day_of_month", d.ID)
			}
		case "day_of_month":
			if d.Due.Day < 1 || d.Due.Day > 31 {
				return fmt.Errorf("deadline %s: day must be 1..31", d.ID)
			}
		default:
			return fmt.Errorf("deadline %s: rule must be last_day_of_month or day_of_month", d.ID)
		}
	}
	if s := p.HolidayShift; s != nil {
		if err := p.checkRow("holiday_shift", s.Citation, s.Corpus); err != nil {
			return err
		}
		if s.Rule != "next_working_day" {
			return errors.New("holiday_shift: next_working_day is the only rule")
		}
		for _, h := range s.Holidays {
			if h.Date.IsZero() || h.Name == "" {
				return errors.New("holiday_shift: every holiday needs a date and a name")
			}
		}
	}
	return nil
}

func (p Pack) validateFields() error {
	if len(p.Fields) == 0 {
		return errors.New("an invoice_fields pack needs at least one field")
	}
	ids := map[string]bool{}
	for _, f := range p.Fields {
		if err := p.checkRow(f.ID, f.Citation, f.Corpus); err != nil {
			return err
		}
		if ids[f.ID] {
			return fmt.Errorf("row id %s repeats", f.ID)
		}
		ids[f.ID] = true
		if f.Path == "" || f.Label == "" {
			return fmt.Errorf("field %s: path and label are required", f.ID)
		}
		switch f.Required {
		case "always":
			if len(f.When) > 0 {
				return fmt.Errorf("field %s: when only goes with required: when", f.ID)
			}
		case "when":
			if len(f.When) == 0 {
				return fmt.Errorf("field %s: required: when needs a when", f.ID)
			}
			for k := range f.When {
				if !slices.Contains(whenKeys, k) {
					return fmt.Errorf("field %s: when key %q is not one of %v", f.ID, k, whenKeys)
				}
			}
		default:
			return fmt.Errorf("field %s: required must be always or when", f.ID)
		}
		if !slices.Contains(fieldChecks, f.Check) {
			return fmt.Errorf("field %s: check must be one of %v", f.ID, fieldChecks)
		}
		if (f.Check == "enum") != (len(f.Values) > 0) {
			return fmt.Errorf("field %s: values go with check: enum, and enum needs values", f.ID)
		}
	}
	return nil
}

// rowKeys are the lookup keys of a pack: two packs of the same kind and regime
// in force at the same time must not share one, or a lookup would be ambiguous.
func (p Pack) rowKeys() []string {
	var keys []string
	switch p.Kind {
	case PackKindRate:
		for _, r := range p.Rates {
			keys = append(keys, "rate:"+r.Tax+"|"+r.Method+"|"+r.ActivityGroup)
		}
		for _, t := range p.Thresholds {
			keys = append(keys, "threshold:"+t.Name)
		}
	case PackKindDeadline:
		for _, d := range p.Deadlines {
			for _, et := range d.EntityTypes {
				keys = append(keys, d.Tax+"|"+et+"|"+d.Method+"|"+d.Period)
			}
		}
	case PackKindInvoiceFields:
		for _, f := range p.Fields {
			keys = append(keys, f.ID)
		}
	}
	return keys
}

func (p Pack) overlaps(q Pack) bool {
	pEnd, qEnd := farFuture, farFuture
	if p.EffectiveTo != nil {
		pEnd = p.EffectiveTo.Time
	}
	if q.EffectiveTo != nil {
		qEnd = q.EffectiveTo.Time
	}
	return !p.EffectiveFrom.After(qEnd) && !q.EffectiveFrom.After(pEnd)
}

var farFuture = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

func checkOverlaps(packs []Pack) error {
	for i := range packs {
		for j := i + 1; j < len(packs); j++ {
			a, b := packs[i], packs[j]
			if a.Kind == PackKindPosting || a.Kind != b.Kind || a.Regime != b.Regime || !a.overlaps(b) {
				continue
			}
			keys := map[string]bool{}
			for _, k := range a.rowKeys() {
				keys[k] = true
			}
			for _, k := range b.rowKeys() {
				if keys[k] {
					return fmt.Errorf("packs %s and %s overlap in time and both define %q", a.Version, b.Version, k)
				}
			}
		}
	}
	// Rows inside one pack must not repeat a key either.
	for _, p := range packs {
		seen := map[string]bool{}
		for _, k := range p.rowKeys() {
			if seen[k] {
				return fmt.Errorf("pack %s defines %q twice", p.Version, k)
			}
			seen[k] = true
		}
	}
	return nil
}

// InForce lists the packs of a kind and regime in force on date, newest
// effective date first. An empty regime matches every regime.
func (e *Engine) InForce(kind, regime string, date time.Time) []*Pack {
	var out []*Pack
	for i := range e.packs {
		p := &e.packs[i]
		if p.Kind == kind && (regime == "" || p.Regime == regime) && p.inForce(date) {
			out = append(out, p)
		}
	}
	return out
}

// RateMatch is a rate row with the pack it came from.
type RateMatch struct {
	Pack *Pack
	Row  RateRow
}

// Rate finds the row for (tax, method, activity group) among the rate packs of
// the regime in force on date; ErrNoRule when none does.
func (e *Engine) Rate(regime, tax, method, group string, date time.Time) (RateMatch, error) {
	for _, p := range e.InForce(PackKindRate, regime, date) {
		for _, r := range p.Rates {
			if r.Tax == tax && r.Method == method && r.ActivityGroup == group {
				return RateMatch{Pack: p, Row: r}, nil
			}
		}
	}
	return RateMatch{}, ErrNoRule
}

// Groups lists the rate rows of (tax, method) in force on date, so a caller
// can name the activity groups an argument may take.
func (e *Engine) Groups(regime, tax, method string, date time.Time) []RateMatch {
	var out []RateMatch
	for _, p := range e.InForce(PackKindRate, regime, date) {
		for _, r := range p.Rates {
			if r.Tax == tax && r.Method == method {
				out = append(out, RateMatch{Pack: p, Row: r})
			}
		}
	}
	return out
}

// ThresholdMatch is a threshold with its pack.
type ThresholdMatch struct {
	Pack *Pack
	Row  Threshold
}

// Threshold finds a named threshold of the regime in force on date.
func (e *Engine) Threshold(regime, name string, date time.Time) (ThresholdMatch, bool) {
	for _, p := range e.InForce(PackKindRate, regime, date) {
		for _, t := range p.Thresholds {
			if t.Name == name {
				return ThresholdMatch{Pack: p, Row: t}, true
			}
		}
	}
	return ThresholdMatch{}, false
}

// ApplyRate is amount × rate_bp / 10000 rounded half-up to the đồng — the
// engine's convention pending the reviewing accountant's confirmation (DESIGN.md
// §5.4). Split so revenue up to 10^15 cannot overflow int64.
func ApplyRate(amount, rateBP int64) int64 {
	if amount < 0 || rateBP < 0 {
		return 0
	}
	q, r := amount/10000, amount%10000
	return q*rateBP + (r*rateBP+5000)/10000
}

// Period is a filing period: a month (1–12), a quarter (1–4) or a year.
type Period struct {
	Kind   string `yaml:"kind" json:"kind"`
	Year   int    `yaml:"year" json:"year"`
	Number int    `yaml:"number" json:"number,omitempty"`
}

// Validate checks the period's shape.
func (p Period) Validate() error {
	if p.Year < 2020 || p.Year > 2100 {
		return fmt.Errorf("period.year must be 2020..2100: %w", errInvalid)
	}
	switch p.Kind {
	case PeriodMonth:
		if p.Number < 1 || p.Number > 12 {
			return fmt.Errorf("period.number must be 1..12 for a month: %w", errInvalid)
		}
	case PeriodQuarter:
		if p.Number < 1 || p.Number > 4 {
			return fmt.Errorf("period.number must be 1..4 for a quarter: %w", errInvalid)
		}
	case PeriodYear:
		if p.Number != 0 {
			return fmt.Errorf("period.number is absent for a year: %w", errInvalid)
		}
	default:
		return fmt.Errorf("period.kind must be month, quarter or year: %w", errInvalid)
	}
	return nil
}

// First is the period's first day.
func (p Period) First() time.Time {
	switch p.Kind {
	case PeriodMonth:
		return time.Date(p.Year, time.Month(p.Number), 1, 0, 0, 0, 0, time.UTC)
	case PeriodQuarter:
		return time.Date(p.Year, time.Month(3*(p.Number-1)+1), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(p.Year, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// Last is the period's last day.
func (p Period) Last() time.Time {
	switch p.Kind {
	case PeriodMonth:
		return p.First().AddDate(0, 1, -1)
	case PeriodQuarter:
		return p.First().AddDate(0, 3, -1)
	}
	return time.Date(p.Year, time.December, 31, 0, 0, 0, 0, time.UTC)
}

// DeadlineQuery asks for the due date of one filing.
type DeadlineQuery struct {
	Tax        string `yaml:"tax" json:"tax"`
	EntityType string `yaml:"entity_type" json:"entity_type"`
	Method     string `yaml:"method" json:"method"`
	Period     Period `yaml:"period" json:"period"`
}

// DeadlineResult is the due date, before and after the holiday shift.
type DeadlineResult struct {
	Pack      *Pack
	Row       DeadlineRow
	Due       time.Time
	Unshifted time.Time
	Shifted   bool
	Holiday   *Holiday // the listed holiday the shift stepped over, if any
}

// Deadline selects the deadline pack in force on the period's last day and
// computes the due date. A row with a method beats a row for any method.
func (e *Engine) Deadline(q DeadlineQuery) (DeadlineResult, error) {
	if err := q.Period.Validate(); err != nil {
		return DeadlineResult{}, err
	}
	last := q.Period.Last()
	var (
		best     *DeadlineRow
		bestPack *Pack
	)
	for _, p := range e.InForce(PackKindDeadline, "", last) {
		for i := range p.Deadlines {
			d := &p.Deadlines[i]
			if d.Tax != q.Tax || d.Period != q.Period.Kind || !slices.Contains(d.EntityTypes, q.EntityType) {
				continue
			}
			if d.Method != "" && d.Method != q.Method {
				continue
			}
			if best == nil || (best.Method == "" && d.Method != "") {
				best, bestPack = d, p
			}
		}
	}
	if best == nil {
		return DeadlineResult{}, ErrNoRule
	}
	target := time.Date(last.Year(), last.Month()+time.Month(best.Due.MonthsAfterPeriod), 1, 0, 0, 0, 0, time.UTC)
	lastOfTarget := target.AddDate(0, 1, -1)
	due := lastOfTarget
	if best.Due.Rule == "day_of_month" && best.Due.Day < lastOfTarget.Day() {
		due = time.Date(target.Year(), target.Month(), best.Due.Day, 0, 0, 0, 0, time.UTC)
	}
	res := DeadlineResult{Pack: bestPack, Row: *best, Due: due, Unshifted: due}
	if s := bestPack.HolidayShift; s != nil {
		for {
			h := s.holiday(res.Due)
			weekend := res.Due.Weekday() == time.Saturday || res.Due.Weekday() == time.Sunday
			if h == nil && !weekend {
				break
			}
			if h != nil && res.Holiday == nil {
				res.Holiday = h
			}
			res.Due = res.Due.AddDate(0, 0, 1)
			res.Shifted = true
		}
	}
	return res, nil
}

func (s *HolidayShift) holiday(day time.Time) *Holiday {
	for i := range s.Holidays {
		if s.Holidays[i].Date.Equal(day) {
			return &s.Holidays[i]
		}
	}
	return nil
}

// FieldIssue is one missing or invalid invoice field.
type FieldIssue struct {
	FieldID string `yaml:"field_id" json:"field_id"`
	Path    string `yaml:"path" json:"path"`
	Label   string `yaml:"label" json:"label"`
	Reason  string `yaml:"reason" json:"reason,omitempty"`
}

// InvoiceCheck is the outcome of checking a draft against the invoice_fields
// packs in force on its issue date.
type InvoiceCheck struct {
	Packs   []*Pack
	Rows    []FieldRow // the rows that applied
	Missing []FieldIssue
	Invalid []FieldIssue
}

// CheckInvoice resolves each field row's path in the draft (a decoded JSON
// object) and applies its check. context carries the when keys (buyer_kind).
func (e *Engine) CheckInvoice(date time.Time, draft map[string]any, context map[string]string) (InvoiceCheck, error) {
	packs := e.InForce(PackKindInvoiceFields, "", date)
	if len(packs) == 0 {
		return InvoiceCheck{}, ErrNoRule
	}
	res := InvoiceCheck{Packs: packs, Missing: []FieldIssue{}, Invalid: []FieldIssue{}}
	for _, p := range packs {
		for _, f := range p.Fields {
			if f.Required == "when" && !whenHolds(f.When, context) {
				continue
			}
			res.Rows = append(res.Rows, f)
			values, found := resolvePath(draft, f.Path)
			issue := FieldIssue{FieldID: f.ID, Path: f.Path, Label: f.Label}
			if !found {
				res.Missing = append(res.Missing, issue)
				continue
			}
			for _, v := range values {
				if !present(v) {
					res.Missing = append(res.Missing, issue)
					break
				}
				if reason := checkValue(f, v); reason != "" {
					issue.Reason = reason
					res.Invalid = append(res.Invalid, issue)
					break
				}
			}
		}
	}
	return res, nil
}

func whenHolds(when, context map[string]string) bool {
	for k, v := range when {
		if context[k] != v {
			return false
		}
	}
	return true
}

// resolvePath walks "a.b" through objects and "lines[].name" through every
// element of an array. found is false when a step is absent or an array is
// empty.
func resolvePath(root any, path string) ([]any, bool) {
	current := []any{root}
	for _, step := range strings.Split(path, ".") {
		each := strings.HasSuffix(step, "[]")
		key := strings.TrimSuffix(step, "[]")
		var next []any
		for _, node := range current {
			obj, ok := node.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := obj[key]
			if !ok {
				return nil, false
			}
			if each {
				arr, ok := v.([]any)
				if !ok || len(arr) == 0 {
					return nil, false
				}
				next = append(next, arr...)
				continue
			}
			next = append(next, v)
		}
		current = next
	}
	return current, len(current) > 0
}

func present(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func checkValue(f FieldRow, v any) string {
	switch f.Check {
	case "mst":
		s, ok := v.(string)
		if !ok || !mstPattern.MatchString(strings.TrimSpace(s)) {
			return "mst_format"
		}
	case "date":
		s, ok := v.(string)
		if !ok {
			return "not_a_date"
		}
		if _, err := time.Parse(DateLayout, strings.TrimSpace(s)); err != nil {
			return "not_a_date"
		}
	case "positive_int", "non_negative_int":
		n, ok := integer(v)
		if !ok {
			return "not_an_integer"
		}
		if f.Check == "positive_int" && n <= 0 {
			return "not_positive"
		}
		if n < 0 {
			return "negative"
		}
	case "enum":
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Values, s) {
			return "not_allowed"
		}
	}
	return ""
}

// integer accepts the numeric types of encoding/json (with UseNumber or
// without) and of the YAML decoder, and only whole values.
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > 1<<62 {
			return 0, false
		}
		return int64(n), true //nolint:gosec // bounded above
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	case interface{ Int64() (int64, error) }:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// IsFixture reports a pack that cites no law.
func (p Pack) IsFixture() bool { return p.LegalDoc == FixtureDoc }

// CorpusRefs lists every corpus reference of the pack.
func (p Pack) CorpusRefs() []string {
	var refs []string
	add := func(r string) {
		if r != "" {
			refs = append(refs, r)
		}
	}
	for _, r := range p.Rules {
		add(r.Corpus)
	}
	for _, r := range p.Rates {
		add(r.Corpus)
	}
	for _, t := range p.Thresholds {
		add(t.Corpus)
	}
	for _, d := range p.Deadlines {
		add(d.Corpus)
	}
	if p.HolidayShift != nil {
		add(p.HolidayShift.Corpus)
	}
	for _, f := range p.Fields {
		add(f.Corpus)
	}
	return refs
}

// CheckCorpus verifies that every corpus reference names an article file
// under legalDir/corpus (DESIGN.md §4: the MCP server checks it at start, the
// regression test when the corpus is present).
func (e *Engine) CheckCorpus(exists func(ref string) bool) error {
	var missing []string
	for _, p := range e.packs {
		for _, ref := range p.CorpusRefs() {
			if !exists(ref) {
				missing = append(missing, p.Version+" → "+ref)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("corpus references without an article: %s", strings.Join(missing, ", "))
	}
	return nil
}
