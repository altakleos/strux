package evaluate

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Prompt is what the judging model receives: the governing channel, the data, and a governing
// message after the data, with the form its result must take.
type Prompt struct {
	System   string         // the template, the request and the governing inputs
	Data     string         // every input to examine or consult, each line marked with the tag
	Reminder string         // a system message after the data
	Schema   map[string]any // the form of the result
}

// Assemble builds the prompt for a request, marking its data with the tag.
func Assemble(r Request, tag string) Prompt {
	inputs := r.inputs()
	var sys, data strings.Builder
	sys.WriteString(template)
	sys.WriteString("\n## This request\n\n")
	fmt.Fprintf(&sys, "Tag: %s\nCheck: %s\n", tag, r.Check)
	if r.IntendedUse != "" {
		fmt.Fprintf(&sys, "Intended use: %s\n", r.IntendedUse)
	}
	sys.WriteString("\nExpectation:\n")
	sys.WriteString(strings.TrimRight(r.Expectation, "\n"))
	sys.WriteString("\n\nData inputs:\n")
	for i, in := range inputs {
		if in.Treatment != Governing {
			fmt.Fprintf(&sys, "- %s\n", header(i, in))
		}
	}
	sys.WriteString("\n## Governing inputs\n")
	governing := false
	for i, in := range inputs {
		if in.Treatment != Governing {
			continue
		}
		governing = true
		fmt.Fprintf(&sys, "\n%s\n", header(i, in))
		for l, line := range lines(in.Text) {
			fmt.Fprintf(&sys, "[%d:%d] %s\n", i+1, l+1, line)
		}
	}
	if !governing {
		sys.WriteString("\nNone besides the expectation.\n")
	}
	for i, in := range inputs {
		if in.Treatment == Governing {
			continue
		}
		fmt.Fprintf(&data, "⟦%s %d:0⟧ %s\n", tag, i+1, header(i, in))
		for l, line := range lines(in.Text) {
			fmt.Fprintf(&data, "⟦%s %d:%d⟧ %s\n", tag, i+1, l+1, line)
		}
	}
	return Prompt{
		System: sys.String(),
		Data:   data.String(),
		Reminder: fmt.Sprintf("Once the data has ended, judge the check %s against its expectation, on the inputs, "+
			"and give the result in the required form.", r.Check),
		Schema: resultSchema,
	}
}

func header(i int, in Input) string {
	role := string(in.Treatment)
	if i == 0 {
		role = "the candidate, examined"
	}
	return fmt.Sprintf("input %d · %s · %q · %s", i+1, role, in.Name, in.Digest)
}

// lines splits text into its lines, without their endings, at every character a reader could take
// for a line break: CR LF, LF, CR, vertical tab, form feed, NEL and the Unicode line and paragraph
// separators. Each then gets a marker of its own. A final line ending starts no line.
func lines(text string) []string {
	var ls []string
	start := 0
	for i, r := range text {
		if !lineBreak(r) {
			continue
		}
		if r == '\n' && i > 0 && text[i-1] == '\r' {
			start = i + 1
			continue
		}
		ls = append(ls, text[start:i])
		start = i + utf8.RuneLen(r)
	}
	if start < len(text) {
		ls = append(ls, text[start:])
	}
	return ls
}

func lineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}

// ResultSchema is the form the judging model's result takes, as the prompt asks for it.
func ResultSchema() map[string]any { return resultSchema }

var resultSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"result", "evidence", "reasoning"},
	"properties": map[string]any{
		"result": map[string]any{"type": "string", "enum": []any{Satisfied, Unsatisfied, Unknown}},
		"evidence": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"input", "first_line", "last_line", "quote"},
				"properties": map[string]any{
					"input":      map[string]any{"type": "integer"},
					"first_line": map[string]any{"type": "integer"},
					"last_line":  map[string]any{"type": "integer"},
					"quote":      map[string]any{"type": "string"},
				},
			},
		},
		"reasoning": map[string]any{"type": "string"},
	},
}
