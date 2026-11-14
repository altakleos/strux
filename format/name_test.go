package format

import (
	"strings"
	"testing"
)

func TestMatchSegmentsWildcards(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"docs/**", "docs/a/b/c.md", true},
		{"docs/**", "docs", true},
		{"docs/**", "other/a.md", false},
		{"**/c.md", "c.md", true},
		{"**/c.md", "docs/a/c.md", true},
		{"**/c.md", "docs/a/d.md", false},
		{"docs/**/c.md", "docs/c.md", true},
		{"docs/**/c.md", "docs/a/b/c.md", true},
		{"docs/**/c.md", "docs/a/b/d.md", false},
		{"docs/*", "docs/a.md", true},
		{"docs/*", "docs/a/b.md", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/a.txt", false},
		{"docs/a*c", "docs/abbc", true},
		{"docs/a*c", "docs/ac", true},
		{"docs/a*c", "docs/ab", false},
		{"**", "any/thing", true},
		{"a/**/**/b", "a/b", true},
		{"Docs/*", "docs/a", false},
	} {
		got := matchSegments(strings.Split(tc.pattern, "/"), strings.Split(tc.name, "/"))
		if got != tc.want {
			t.Errorf("%q against %q: got %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestNameSegmentsRefuseEmptySegmentsAndNormalize(t *testing.T) {
	for _, bad := range []string{"", "/a", "a/", "a//b"} {
		if _, err := nameSegments(bad, "name"); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
	composed, err := nameSegments("café/a", "name")
	if err != nil {
		t.Fatal(err)
	}
	decomposed, err := nameSegments("café/a", "name")
	if err != nil {
		t.Fatal(err)
	}
	if composed[0] != decomposed[0] {
		t.Errorf("NFC spellings differ: %q and %q", composed[0], decomposed[0])
	}
}

// The library functions run through the reference implementation, so a name and a take are
// checked end to end, not only as Go functions.
func TestLibraryFunctionsEvaluate(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  value:\n    purpose: x\n    types:\n      inner:\n        fields:\n          label: { type: string }\n          items: { type: list(integer) }\n    structure:\n      name: { type: name }\n      names: { type: list(name-pattern) }\n      items: { type: list(integer) }\n      combo: { type: combination }\n"
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u, problems := Prepare(map[string]*Package{"demo": Build("demo", "demo.yaml", content)})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for _, tc := range []struct {
		expr, want string
		self       map[string]any
	}{
		{"matches_name('docs/**', self.name)", "true", map[string]any{"name": "docs/a/b.md"}},
		{"matches_name('docs/*', self.name)", "false", map[string]any{"name": "docs/a/b.md"}},
		{"matches_name('docs/*', 'docs//a')", "unknown", nil},
		{"matches_name('docs/a**b', 'docs/ab')", "unknown", nil},
		{"self.names.exists(p, matches_name(p, 'docs/a.md'))", "true", map[string]any{"names": []any{"docs/*.md"}}},
		{"self.items.take(2) == [1, 2]", "true", map[string]any{"items": []any{int64(1), int64(2), int64(3)}}},
		{"self.items.take(9) == [1, 2]", "true", map[string]any{"items": []any{int64(1), int64(2)}}},
		{"self.items.take(0) == []", "true", map[string]any{"items": []any{int64(1)}}},
		{"self.items.take(-1) == []", "unknown", map[string]any{"items": []any{int64(1)}}},
		{"size(self.items.take(3)) == 0", "true", map[string]any{}},
		// A value constructed by naming its type is held as an instance is.
		{"strux.combination{rule: 'unanimous', cap: 1}.cap == 1", "true", nil},
		{"demo.value.inner{label: 'x'}.label == 'x'", "true", nil},
		{"demo.value.inner{label: 'x', items: [1, 2]}.items == [1, 2]", "true", nil},
		{"!has(demo.value.inner{label: 'x'}.items) && size(demo.value.inner{label: 'x'}.items) == 0", "true", nil},
		{"demo.value.inner{label: 'x'} == demo.value.inner{label: 'x'} && demo.value.inner{label: 'x'} != demo.value.inner{label: 'y'}", "true", nil},
		// combine settles nothing for no findings, whichever combination it is given.
		{"combine([], self.combo)", "unknown", map[string]any{"combo": map[string]any{"rule": "unanimous", "cap": int64(2)}}},
		{"combine([], strux.combination{rule: 'majority', cap: 3}) || true", "true", nil},
	} {
		got, problems := u.EvaluateCheck("demo", "value", nil, tc.expr, tc.self)
		if len(problems) > 0 {
			t.Errorf("%s: %v", tc.expr, problems)
			continue
		}
		if got.Value != tc.want {
			t.Errorf("%s: got %s (%s), want %s", tc.expr, got.Value, got.Reason, tc.want)
		}
	}
}

func TestIndependentKey(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\n"
	decision := "  decision:\n    purpose: A decision about one use of one candidate.\n    governing: true\n    structure:\n      use: { type: string, required: true }\n      candidate: { type: string, required: true }\n"
	plain := "  plain:\n    purpose: A definition with no use or candidate field.\n    structure:\n      body: { type: text }\n"
	subject := func(use string) string {
		return "  note:\n    purpose: x\n    structure:\n      authors: { type: list(principal) }\n    uses:\n      read:\n" + use + "        requires:\n          c: { rule: x, expr: \"true\" }\n"
	}
	for name, tc := range map[string]struct{ src, want string }{
		"off by default":         {head + "definitions:\n" + subject("        beneficiaries: self.authors\n"), ""},
		"kinds as a literal":     {head + "definitions:\n" + decision + subject("        beneficiaries: self.authors\n        independent: \"['demo/decision']\"\n"), ""},
		"kinds computed":         {head + "definitions:\n" + decision + subject("        beneficiaries: self.authors\n        independent: \"size(self.authors) > 1 ? ['demo/decision'] : []\"\n"), ""},
		"not a list":             {head + "definitions:\n" + subject("        beneficiaries: self.authors\n        independent: \"true\"\n"), "independent: is bool, not list(string)"},
		"without beneficiaries":  {head + "definitions:\n" + decision + subject("        independent: \"['demo/decision']\"\n"), "needs beneficiaries"},
		"beneficiaries none":     {head + "definitions:\n" + decision + subject("        beneficiaries: \"[]\"\n        independent: \"['demo/decision']\"\n"), "benefits no party"},
		"no such kind":           {head + "definitions:\n" + subject("        beneficiaries: self.authors\n        independent: \"['demo/missing']\"\n"), "demo/missing is not a definition"},
		"computed, no such kind": {head + "definitions:\n" + subject("        beneficiaries: self.authors\n        independent: \"size(self.authors) > 1 ? ['demo/missing'] : []\"\n"), "demo/missing is not a definition"},
		"computed, no fields":    {head + "definitions:\n" + plain + subject("        beneficiaries: self.authors\n        independent: \"size(self.authors) > 1 ? ['demo/plain'] : []\"\n"), "has no string field use"},
		"accepted, no such kind": {head + "definitions:\n" + subject("        beneficiaries: self.authors\n        accepts_declared: \"['demo/missing']\"\n"), "demo/missing is not a definition"},
		"accepted, computed":     {head + "definitions:\n" + subject("        beneficiaries: self.authors\n        accepts_declared: \"size(self.authors) > 1 ? ['demo/missing'] : []\"\n"), "demo/missing is not a definition"},
		"a decision point":       {head + "decision_points:\n  gate: { meaning: x }\n" + "definitions:\n" + subject("        beneficiaries: self.authors\n        independent: \"['demo/gate']\"\n"), "demo/gate is not a definition"},
		"kind without fields":    {head + "definitions:\n" + plain + subject("        beneficiaries: self.authors\n        independent: \"['demo/plain']\"\n"), "has no string field use"},
	} {
		got := checkSource(t, "demo", tc.src)
		switch {
		case tc.want == "" && got != "":
			t.Errorf("%s: unexpected errors:\n%s", name, got)
		case tc.want != "" && !strings.Contains(got, tc.want):
			t.Errorf("%s: want %q, got:\n%s", name, tc.want, got)
		}
	}
}
