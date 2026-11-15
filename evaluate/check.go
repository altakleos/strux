package evaluate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Judgment is the judging model's result, in the form the schema fixes.
type Judgment struct {
	Result    string     `json:"result"`
	Evidence  []Citation `json:"evidence"`
	Reasoning string     `json:"reasoning"`
}

// Citation points to lines of one input, with a quote from them.
type Citation struct {
	Input     int    `json:"input"`
	FirstLine int    `json:"first_line"`
	LastLine  int    `json:"last_line"`
	Quote     string `json:"quote"`
}

// Bounds on a judgment, so that a result stays short enough to read and to sign. The template
// states them to the model.
const (
	maxCitations = 32
	maxSpan      = 400  // lines one citation covers
	maxQuote     = 600  // characters
	maxReasoning = 6000 // characters
)

// check is the step between the judging model and the finding. It accepts a judgment only in the
// schema's exact form, with a result the finding kind allows, and with every citation inside the
// request's inputs and quoting what those lines hold. Each quote it returns is as matched: without
// markers, and with its white space collapsed.
func check(inputs []Input, tag, text string) (Judgment, error) {
	if err := form(text); err != nil {
		return Judgment{}, fmt.Errorf("the result is not in the required form: %w", err)
	}
	var j Judgment
	if err := json.Unmarshal([]byte(text), &j); err != nil {
		return Judgment{}, fmt.Errorf("the result is not in the required form: %w", err)
	}
	if !slices.Contains([]string{Satisfied, Unsatisfied, Unknown}, j.Result) {
		return Judgment{}, fmt.Errorf("the result %q is not satisfied, unsatisfied or unknown", j.Result)
	}
	if n := utf8.RuneCountInString(j.Reasoning); strings.TrimSpace(j.Reasoning) == "" || n > maxReasoning {
		return Judgment{}, fmt.Errorf("the reasoning has %d characters, and must have from 1 to %d", n, maxReasoning)
	}
	if len(j.Evidence) > maxCitations {
		return Judgment{}, fmt.Errorf("the result cites %d passages, more than %d", len(j.Evidence), maxCitations)
	}
	candidate := false
	for k, c := range j.Evidence {
		if c.Input < 1 || c.Input > len(inputs) {
			return Judgment{}, fmt.Errorf("citation %d names input %d, which the request does not hold", k+1, c.Input)
		}
		in := inputs[c.Input-1]
		ls := lines(in.Text)
		if c.FirstLine < 1 || c.LastLine < c.FirstLine || c.LastLine > len(ls) {
			return Judgment{}, fmt.Errorf("citation %d names lines %d to %d of input %d, which has %d", k+1, c.FirstLine, c.LastLine, c.Input, len(ls))
		}
		if c.LastLine-c.FirstLine >= maxSpan {
			return Judgment{}, fmt.Errorf("citation %d covers more than %d lines", k+1, maxSpan)
		}
		if utf8.RuneCountInString(c.Quote) > maxQuote {
			return Judgment{}, fmt.Errorf("citation %d quotes more than %d characters", k+1, maxQuote)
		}
		q := squeeze(unmark(c.Quote, tag, in.Treatment == Governing))
		if !strings.ContainsFunc(q, wordRune) {
			return Judgment{}, fmt.Errorf("citation %d quotes no letter or digit", k+1)
		}
		if !holds(squeeze(strings.Join(ls[c.FirstLine-1:c.LastLine], "\n")), q) {
			return Judgment{}, fmt.Errorf("citation %d quotes text that lines %d to %d of input %d do not hold", k+1, c.FirstLine, c.LastLine, c.Input)
		}
		j.Evidence[k].Quote = q
		candidate = candidate || c.Input == 1
	}
	if j.Result != Unknown && !candidate {
		return Judgment{}, fmt.Errorf("a result of %s cites no line of the candidate", j.Result)
	}
	return j, nil
}

// form checks the result's form exactly, as the JSON decoder alone does not: each key spelled as
// the schema spells it and given once, each value of its type and never null, and an integer
// written as one.
func form(text string) error {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := object(dec, map[string]func(*json.Decoder) error{
		"result":    stringValue,
		"reasoning": stringValue,
		"evidence": func(dec *json.Decoder) error {
			return array(dec, func(dec *json.Decoder) error {
				return object(dec, map[string]func(*json.Decoder) error{
					"input":      integer,
					"first_line": integer,
					"last_line":  integer,
					"quote":      stringValue,
				})
			})
		},
	}); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("more text follows it")
	}
	return nil
}

func object(dec *json.Decoder, fields map[string]func(*json.Decoder) error) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("an object was expected")
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := t.(string)
		value, ok := fields[key]
		switch {
		case !ok:
			return fmt.Errorf("the key %q is not in the schema", key)
		case seen[key]:
			return fmt.Errorf("the key %q is given twice", key)
		}
		seen[key] = true
		if err := value(dec); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if len(seen) != len(fields) {
		return errors.New("a key the schema requires is missing")
	}
	return nil
}

func array(dec *json.Decoder, item func(*json.Decoder) error) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return errors.New("an array was expected")
	}
	for dec.More() {
		if err := item(dec); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}

func stringValue(dec *json.Decoder) error {
	if t, err := dec.Token(); err != nil {
		return err
	} else if _, ok := t.(string); !ok {
		return errors.New("a string was expected")
	}
	return nil
}

var integerForm = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,8})$`)

func integer(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if n, ok := t.(json.Number); !ok || !integerForm.MatchString(n.String()) {
		return errors.New("an integer was expected")
	}
	return nil
}

// unmark removes from a quote the markers that the prompt put before each line it spans: the
// request's tag for data, and the plain line numbers of a governing input.
func unmark(quote, tag string, governing bool) string {
	marker := regexp.MustCompile(`⟦` + regexp.QuoteMeta(tag) + ` [0-9]+:[0-9]+⟧`)
	if governing {
		marker = regexp.MustCompile(`\[[0-9]+:[0-9]+\]`)
	}
	return marker.ReplaceAllString(quote, " ")
}

// holds reports whether the text holds the quote as whole words: a quote that starts or ends with a
// letter or digit does not match within a longer word.
func holds(text, quote string) bool {
	first, _ := utf8.DecodeRuneInString(quote)
	last, _ := utf8.DecodeLastRuneInString(quote)
	for from := 0; ; {
		i := strings.Index(text[from:], quote)
		if i < 0 {
			return false
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(quote):])
		splitsWordBefore := wordRune(first) && i > 0 && wordRune(before)
		splitsWordAfter := wordRune(last) && i+len(quote) < len(text) && wordRune(after)
		if !splitsWordBefore && !splitsWordAfter {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		from = i + size
	}
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// squeeze collapses every run of white space to one space, so a quote matches however its lines
// were wrapped.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

// plain replaces the characters that could change how a reader sees a finding's text, such as
// control characters and bidirectional overrides, keeping line breaks and tabs.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return '�'
		}
		return r
	}, s)
}

// finding makes the finding for a checked judgment.
func finding(r Request, j Judgment) Finding {
	inputs := r.inputs()
	var ev strings.Builder
	for k, c := range j.Evidence {
		if k > 0 {
			ev.WriteString("\n")
		}
		fmt.Fprintf(&ev, "%s, lines %d to %d: %q", inputs[c.Input-1].Name, c.FirstLine, c.LastLine, c.Quote)
	}
	return Finding{
		Check:     r.Check,
		Result:    j.Result,
		BoundTo:   r.boundTo(),
		Request:   r.ID,
		Evidence:  ev.String(),
		Reasoning: plain(j.Reasoning),
	}
}
