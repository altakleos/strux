package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/altakleos/strux/evaluate"
)

// Model is one model's settings in the suite file: how it is reached, which model, its effort and
// output limit, and, for a backend billed by use, its price.
type Model struct {
	Backend   string `yaml:"backend"`
	Model     string `yaml:"model"`
	Effort    string `yaml:"effort"`
	MaxTokens int64  `yaml:"max_tokens"`
	Price     *Price `yaml:"price"`
}

// anthropicAPI is the backend that reaches the Anthropic Messages API with an API key, billed by
// use; every other backend is an adapter the suite names.
const anthropicAPI = "anthropic-api"

func (m Model) config(program string) evaluate.Config {
	return evaluate.Config{Backend: m.Backend, Model: m.Model, Effort: m.Effort, MaxTokens: m.MaxTokens, Program: program}
}

// Technique is one entry of the catalog of attacks.
type Technique struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Price is what a model costs, in dollars for each million tokens.
type Price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// Suite is what the suite owner freezes before the first run: the configuration under test, the
// attacker and the adjudicator, the budget and the rule of the attack, the decision rule, the
// catalog and the instructions of each model the suite drives.
type Suite struct {
	Evaluator   Model `yaml:"evaluator"`
	Attacker    Model `yaml:"attacker"`
	Adjudicator Model `yaml:"adjudicator"`

	Queries     int    `yaml:"queries"`     // replica queries in each adaptive attack
	Submissions int    `yaml:"submissions"` // variants submitted, the cap of the adequacy rule
	Rule        string `yaml:"rule"`        // how the submitted findings combine

	Bound          float64 `yaml:"bound"`           // the upper bound of the break rate that passes
	AccuracyFloor  float64 `yaml:"accuracy_floor"`  // accuracy on adequate cases without attack
	AccuracyMargin float64 `yaml:"accuracy_margin"` // how far below the reference's accuracy it may lie
	MinInadequate  int     `yaml:"min_inadequate"`
	MinAdequate    int     `yaml:"min_adequate"`

	Techniques              []Technique         `yaml:"techniques"`
	AttackerInstructions    string              `yaml:"attacker_instructions"`
	AdjudicatorInstructions string              `yaml:"adjudicator_instructions"`
	Adapters                map[string][]string `yaml:"adapters"` // each adapter backend's command

	digest string
}

// loadSuite reads and checks a suite file.
func loadSuite(path string) (Suite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var s Suite
	if err := strict(b, &s); err != nil {
		return Suite{}, fmt.Errorf("%s: %w", path, err)
	}
	s.digest = digest(b)
	return s, s.valid()
}

func (s Suite) valid() error {
	for role, m := range s.roles() {
		if m.Model == "" || m.MaxTokens < 1 {
			return fmt.Errorf("the %s needs a model and a positive max_tokens", role)
		}
		switch _, adapter := s.Adapters[m.Backend]; {
		case m.Backend == anthropicAPI && m.Price == nil:
			return fmt.Errorf("the %s is billed by use and has no price, so its budget cannot be kept", role)
		case m.Backend != anthropicAPI && !adapter:
			return fmt.Errorf("the %s's backend %q is neither %s nor an adapter the suite names", role, m.Backend, anthropicAPI)
		}
	}
	for name, cmd := range s.Adapters {
		if len(cmd) == 0 {
			return fmt.Errorf("the adapter %s has no command", name)
		}
	}
	switch {
	case s.Adjudicator.Model == s.Evaluator.Model:
		return errors.New("the adjudicator's model is the judging model, and it must differ")
	case s.Queries < 1 || s.Submissions < 1:
		return errors.New("queries and submissions must each be at least 1")
	case !slices.Contains([]string{"unanimous", "all-satisfied", "majority"}, s.Rule):
		return fmt.Errorf("the rule %q is not unanimous, all-satisfied or majority", s.Rule)
	case s.Bound <= 0 || s.Bound >= 1 || s.AccuracyFloor <= 0 || s.AccuracyFloor > 1 || s.AccuracyMargin < 0:
		return errors.New("the bound, the accuracy floor and the margin must be fractions")
	case len(s.Techniques) == 0 || s.AttackerInstructions == "" || s.AdjudicatorInstructions == "":
		return errors.New("the suite needs its catalog and the attacker's and the adjudicator's instructions")
	}
	return nil
}

func (s Suite) roles() map[string]Model {
	return map[string]Model{"evaluator": s.Evaluator, "attacker": s.Attacker, "adjudicator": s.Adjudicator}
}

// prices is each billed backend's price, by backend and model, for the meter.
func (s Suite) prices() map[string]Price {
	p := map[string]Price{}
	for _, m := range s.roles() {
		if m.Price != nil {
			p[m.Backend+"/"+m.Model] = *m.Price
		}
	}
	return p
}

// Case is one bounded review of the set, with its label and the key that says why it has it.
type Case struct {
	ID      string
	Label   string // adequate or inadequate
	Key     string
	Request evaluate.Request
}

// caseFile is a case's case.yaml. Each input's text is the named file beside it.
type caseFile struct {
	Label       string      `yaml:"label"`
	Key         string      `yaml:"key"`
	Check       string      `yaml:"check"`
	Expectation string      `yaml:"expectation"`
	IntendedUse string      `yaml:"intended_use"`
	Candidate   inputFile   `yaml:"candidate"`
	Context     []inputFile `yaml:"context"`
}

type inputFile struct {
	Name      string `yaml:"name"`
	Treatment string `yaml:"treatment"`
	File      string `yaml:"file"`
}

const (
	adequate   = "adequate"
	inadequate = "inadequate"
)

// loadCases reads every case in a directory, one subdirectory each, and the digest of the whole
// set, which changes with any byte of any case.
func loadCases(dir string) ([]Case, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	var cases []Case
	var set strings.Builder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, sum, err := loadCase(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, "", fmt.Errorf("case %s: %w", e.Name(), err)
		}
		c.ID = e.Name()
		cases = append(cases, c)
		fmt.Fprintf(&set, "%s %s\n", c.ID, sum)
	}
	if len(cases) == 0 {
		return nil, "", fmt.Errorf("%s holds no case", dir)
	}
	return cases, digest([]byte(set.String())), nil
}

func loadCase(dir string) (Case, string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "case.yaml"))
	if err != nil {
		return Case{}, "", err
	}
	var f caseFile
	if err := strict(b, &f); err != nil {
		return Case{}, "", err
	}
	if f.Label != adequate && f.Label != inadequate {
		return Case{}, "", fmt.Errorf("the label %q is not adequate or inadequate", f.Label)
	}
	if strings.TrimSpace(f.Key) == "" {
		return Case{}, "", errors.New("the case has no key")
	}
	sums := []string{digest(b)}
	read := func(in inputFile) (evaluate.Input, error) {
		if in.File == "" || filepath.Base(in.File) != in.File {
			return evaluate.Input{}, fmt.Errorf("the input %q names %q, which is not a file beside case.yaml", in.Name, in.File)
		}
		text, err := os.ReadFile(filepath.Join(dir, in.File))
		if err != nil {
			return evaluate.Input{}, err
		}
		sums = append(sums, digest(text))
		return evaluate.Input{Name: in.Name, Digest: digest(text), Treatment: evaluate.Treatment(in.Treatment), Text: string(text)}, nil
	}
	r := evaluate.Request{ID: digest(b), Check: f.Check, Expectation: f.Expectation, IntendedUse: f.IntendedUse}
	if f.Candidate.Treatment == "" {
		f.Candidate.Treatment = string(evaluate.Examined) // a candidate is examined
	}
	if r.Candidate, err = read(f.Candidate); err != nil {
		return Case{}, "", err
	}
	for _, in := range f.Context {
		x, err := read(in)
		if err != nil {
			return Case{}, "", err
		}
		r.Context = append(r.Context, x)
	}
	if err := r.Valid(); err != nil {
		return Case{}, "", err
	}
	return Case{Label: f.Label, Key: f.Key, Request: r}, digest([]byte(strings.Join(sums, " "))), nil
}

// strict decodes YAML that names only the fields the type has.
func strict(b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(v)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
