package rules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/goccy/go-yaml"
)

// Suite is one expected-result table (rules/tests/<article>.yaml). Kind
// (default posting) names the pack kind the table exercises and which case
// list it carries.
type Suite struct {
	Kind          string         `yaml:"kind"`
	Cases         []Case         `yaml:"-"`
	RateCases     []RateCase     `yaml:"-"`
	DeadlineCases []DeadlineCase `yaml:"-"`
	InvoiceCases  []InvoiceCase  `yaml:"-"`
	Path          string         `yaml:"-"`
}

// RateCase checks one rate lookup and its arithmetic.
type RateCase struct {
	Name    string `yaml:"name"`
	Request struct {
		Regime        string    `yaml:"regime"`
		Date          civilDate `yaml:"date"`
		Tax           string    `yaml:"tax"`
		Method        string    `yaml:"method"`
		ActivityGroup string    `yaml:"activity_group"`
		RevenueMinor  int64     `yaml:"revenue_minor"`
	} `yaml:"request"`
	Expect struct {
		Error       string `yaml:"error"`
		PackVersion string `yaml:"pack_version"`
		RowID       string `yaml:"row_id"`
		RateBP      *int64 `yaml:"rate_bp"`
		AmountMinor int64  `yaml:"amount_minor"`
	} `yaml:"expect"`
}

// DeadlineCase checks one due date.
type DeadlineCase struct {
	Name    string        `yaml:"name"`
	Request DeadlineQuery `yaml:"request"`
	Expect  struct {
		Error       string    `yaml:"error"`
		PackVersion string    `yaml:"pack_version"`
		RowID       string    `yaml:"row_id"`
		DueDate     civilDate `yaml:"due_date"`
		Shifted     bool      `yaml:"shifted"`
	} `yaml:"expect"`
}

// InvoiceCase checks one draft invoice.
type InvoiceCase struct {
	Name    string `yaml:"name"`
	Request struct {
		IssueDate civilDate      `yaml:"issue_date"`
		BuyerKind string         `yaml:"buyer_kind"`
		Draft     map[string]any `yaml:"draft"`
	} `yaml:"request"`
	Expect struct {
		Error       string   `yaml:"error"`
		PackVersion string   `yaml:"pack_version"`
		Missing     []string `yaml:"missing"`
		Invalid     []string `yaml:"invalid"`
	} `yaml:"expect"`
}

// Case is one row of the table: the request and what the engine must answer.
type Case struct {
	Name    string  `yaml:"name"`
	Request Request `yaml:"request"`
	Expect  Expect  `yaml:"expect"`
}

// Expect is the expected answer: either an error code or the treatment.
type Expect struct {
	Error              string `yaml:"error"`
	RulePackVersion    string `yaml:"rule_pack_version"`
	RuleID             string `yaml:"rule_id"`
	NeedsCounterpart   bool   `yaml:"needs_counterpart"`
	CounterpartDefault string `yaml:"counterpart_default"`
	Lines              []Line `yaml:"lines"`
}

// LoadSuites reads every table under dir/tests.
func LoadSuites(dir string) ([]Suite, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "tests", "*.y*ml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var suites []Suite
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // the repository's rules/tests directory
		if err != nil {
			return nil, err
		}
		suite, err := parseSuite(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		suite.Path = path
		suites = append(suites, suite)
	}
	if len(suites) == 0 {
		return nil, fmt.Errorf("no expected-result tables under %s", filepath.Join(dir, "tests"))
	}
	return suites, nil
}

// parseSuite reads the kind first, then decodes the table strictly into the
// case type of that kind.
func parseSuite(raw []byte) (Suite, error) {
	var head struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(raw, &head); err != nil {
		return Suite{}, err
	}
	suite := Suite{Kind: head.Kind}
	if suite.Kind == "" {
		suite.Kind = PackKindPosting
	}
	var err error
	switch suite.Kind {
	case PackKindPosting:
		var t struct {
			Kind  string `yaml:"kind"`
			Cases []Case `yaml:"cases"`
		}
		err = yaml.UnmarshalWithOptions(raw, &t, yaml.Strict())
		suite.Cases = t.Cases
	case PackKindRate:
		var t struct {
			Kind  string     `yaml:"kind"`
			Cases []RateCase `yaml:"cases"`
		}
		err = yaml.UnmarshalWithOptions(raw, &t, yaml.Strict())
		suite.RateCases = t.Cases
	case PackKindDeadline:
		var t struct {
			Kind  string         `yaml:"kind"`
			Cases []DeadlineCase `yaml:"cases"`
		}
		err = yaml.UnmarshalWithOptions(raw, &t, yaml.Strict())
		suite.DeadlineCases = t.Cases
	case PackKindInvoiceFields:
		var t struct {
			Kind  string        `yaml:"kind"`
			Cases []InvoiceCase `yaml:"cases"`
		}
		err = yaml.UnmarshalWithOptions(raw, &t, yaml.Strict())
		suite.InvoiceCases = t.Cases
	default:
		return Suite{}, fmt.Errorf("kind %q is not posting, rate, deadline or invoice_fields", suite.Kind)
	}
	return suite, err
}

// Len is the number of cases in the table.
func (s Suite) Len() int {
	return len(s.Cases) + len(s.RateCases) + len(s.DeadlineCases) + len(s.InvoiceCases)
}

// Run evaluates every case and returns one error per failing case.
func (e *Engine) Run(suite Suite) []error {
	var failures []error
	fail := func(name string, err error) {
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %s: %w", filepath.Base(suite.Path), name, err))
		}
	}
	for _, c := range suite.Cases {
		fail(c.Name, e.check(c))
	}
	for _, c := range suite.RateCases {
		fail(c.Name, e.checkRate(c))
	}
	for _, c := range suite.DeadlineCases {
		fail(c.Name, e.checkDeadline(c))
	}
	for _, c := range suite.InvoiceCases {
		fail(c.Name, e.checkInvoice(c))
	}
	return failures
}

// expectError compares an expected error code with the error returned.
func expectError(want string, err error) (done bool, _ error) {
	switch {
	case want == "" && err != nil:
		return true, err
	case want == "":
		return false, nil
	case err == nil:
		return true, fmt.Errorf("expected error %s, got an answer", want)
	case want == ErrNoRule.Error() && errors.Is(err, ErrNoRule):
		return true, nil
	case want == "invalid" && IsInvalid(err):
		return true, nil
	}
	return true, fmt.Errorf("expected error %s, got %w", want, err)
}

func (e *Engine) checkRate(c RateCase) error {
	m, err := e.Rate(c.Request.Regime, c.Request.Tax, c.Request.Method, c.Request.ActivityGroup, c.Request.Date.Time)
	if done, err := expectError(c.Expect.Error, err); done {
		return err
	}
	if m.Pack.Version != c.Expect.PackVersion || m.Row.ID != c.Expect.RowID {
		return fmt.Errorf("got %s / %s, want %s / %s", m.Pack.Version, m.Row.ID, c.Expect.PackVersion, c.Expect.RowID)
	}
	if (m.Row.RateBP == nil) != (c.Expect.RateBP == nil) || (m.Row.RateBP != nil && *m.Row.RateBP != *c.Expect.RateBP) {
		return fmt.Errorf("rate_bp = %v, want %v", deref(m.Row.RateBP), deref(c.Expect.RateBP))
	}
	if m.Row.RateBP != nil {
		if got := ApplyRate(c.Request.RevenueMinor, *m.Row.RateBP); got != c.Expect.AmountMinor {
			return fmt.Errorf("amount_minor = %d, want %d", got, c.Expect.AmountMinor)
		}
	}
	return nil
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func (e *Engine) checkDeadline(c DeadlineCase) error {
	res, err := e.Deadline(c.Request)
	if done, err := expectError(c.Expect.Error, err); done {
		return err
	}
	if res.Pack.Version != c.Expect.PackVersion || res.Row.ID != c.Expect.RowID {
		return fmt.Errorf("got %s / %s, want %s / %s", res.Pack.Version, res.Row.ID, c.Expect.PackVersion, c.Expect.RowID)
	}
	if !res.Due.Equal(c.Expect.DueDate.Time) || res.Shifted != c.Expect.Shifted {
		return fmt.Errorf("due %s (shifted %v), want %s (shifted %v)", res.Due.Format(DateLayout), res.Shifted, c.Expect.DueDate.Format(DateLayout), c.Expect.Shifted)
	}
	return nil
}

func (e *Engine) checkInvoice(c InvoiceCase) error {
	res, err := e.CheckInvoice(c.Request.IssueDate.Time, c.Request.Draft, map[string]string{"buyer_kind": c.Request.BuyerKind})
	if done, err := expectError(c.Expect.Error, err); done {
		return err
	}
	if res.Packs[0].Version != c.Expect.PackVersion {
		return fmt.Errorf("pack %s, want %s", res.Packs[0].Version, c.Expect.PackVersion)
	}
	ids := func(issues []FieldIssue) []string {
		out := []string{}
		for _, i := range issues {
			out = append(out, i.FieldID)
		}
		sort.Strings(out)
		return out
	}
	want := func(s []string) []string {
		out := append([]string{}, s...)
		sort.Strings(out)
		return out
	}
	if got := ids(res.Missing); !reflect.DeepEqual(got, want(c.Expect.Missing)) {
		return fmt.Errorf("missing = %v, want %v", got, want(c.Expect.Missing))
	}
	if got := ids(res.Invalid); !reflect.DeepEqual(got, want(c.Expect.Invalid)) {
		return fmt.Errorf("invalid = %v, want %v", got, want(c.Expect.Invalid))
	}
	return nil
}

func (e *Engine) check(c Case) error {
	resp, err := e.Evaluate(c.Request)
	if c.Expect.Error != "" {
		switch {
		case err == nil:
			return fmt.Errorf("expected error %s, got %s / %s", c.Expect.Error, resp.RulePackVersion, resp.RuleID)
		case c.Expect.Error == ErrNoRule.Error() && errors.Is(err, ErrNoRule):
			return nil
		default:
			return fmt.Errorf("expected error %s, got %w", c.Expect.Error, err)
		}
	}
	if err != nil {
		return err
	}
	if resp.RulePackVersion != c.Expect.RulePackVersion {
		return fmt.Errorf("rule_pack_version = %s, want %s", resp.RulePackVersion, c.Expect.RulePackVersion)
	}
	if resp.RuleID != c.Expect.RuleID {
		return fmt.Errorf("rule_id = %s, want %s", resp.RuleID, c.Expect.RuleID)
	}
	if resp.NeedsCounterpart != c.Expect.NeedsCounterpart || resp.CounterpartDefault != c.Expect.CounterpartDefault {
		return fmt.Errorf("needs_counterpart = %v (%q), want %v (%q)", resp.NeedsCounterpart, resp.CounterpartDefault, c.Expect.NeedsCounterpart, c.Expect.CounterpartDefault)
	}
	if !reflect.DeepEqual(resp.Lines, c.Expect.Lines) {
		return fmt.Errorf("lines = %+v, want %+v", resp.Lines, c.Expect.Lines)
	}
	return nil
}
