package format

import (
	"strings"
	"testing"
)

func TestParseRefusesWhatIsNotInTheSubset(t *testing.T) {
	for name, src := range map[string]string{
		"anchor":       "a: &x 1\nb: 2\n",
		"alias":        "a: &x 1\nb: *x\n",
		"tag":          "a: !!str 1\n",
		"merge":        "a: {x: 1}\nb:\n  <<: {x: 2}\n",
		"duplicate":    "a: 1\na: 2\n",
		"null":         "a:\n",
		"float":        "a: 1.5\n",
		"timestamp":    "a: 2026-10-17\n",
		"hex":          "a: 0x1F\n",
		"too large":    "a: 9007199254740992\n",
		"two docs":     "a: 1\n---\nb: 2\n",
		"key anchor":   "&k a: 1\n",
		"key tag":      "!!str a: 1\n",
		"bare tag":     "a: ! foo\n",
		"boolean key":  "true: 1\n",
		"integer key":  "0x10: a\n",
		"yaml 1.1":     "%YAML 1.1\n---\na: 1\n",
		"1.1 boolean":  "a: yes\n",
		"underscored":  "a: 1_000\n",
		"leading zero": "a: 017\n",
		"empty":        "  \n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: accepted %q", name, src)
		}
	}
	if _, err := Parse([]byte("'<<': 1\n'yes': 2\n")); err != nil {
		t.Errorf("quoted keys refused: %v", err)
	}
	v, err := Parse([]byte("a: 'x'\nb: [1, true, \"2026-10-17\"]\nc: >\n  folded\n  text\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(Canonical(v)); got != `{"a":"x","b":[1,true,"2026-10-17"],"c":"folded text\n"}` {
		t.Errorf("canonical form %s", got)
	}
}

// yaml.v3 counts a parser error's line from 0 and a scanner error's from 1; Parse counts every
// line from 1.
func TestParseNamesTheLineOfASyntaxError(t *testing.T) {
	for src, want := range map[string]string{
		"a: 1\nb: [1\nc: 2\n":  "line 2: did not find expected ',' or ']'",
		"a:\n  b: 1\n  : 2\n":  "line 2: did not find expected key",
		"[1\n":                 "line 2: did not find expected ',' or ']'",
		"a: 1\nb: 2\n  c: 3\n": "line 3: mapping values are not allowed in this context",
		"a: 1\n\tb: 2\n":       "line 2: found a tab character that violates indentation",
		"a: 1\nb: [\n":         "line 3: did not find expected node content",
	} {
		_, err := Parse([]byte(src))
		if err == nil || err.Error() != want {
			t.Errorf("Parse(%q) = %v, want %q", src, err, want)
		}
	}
}

func TestCanonicalOrdersByUTF16(t *testing.T) {
	// U+FFFD sorts before U+1F600 in UTF-8 but after it in UTF-16, which RFC 8785 uses.
	v := map[string]any{"\uFFFD": int64(1), "\U0001F600": int64(2), "a\u0001": "\"\\\n"}
	if got, want := string(Canonical(v)), "{\"a\\u0001\":\"\\\"\\\\\\n\",\"\U0001F600\":2,\"\uFFFD\":1}"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

const pkgSource = `format: 1
package: demo
revision: 1
definitions:
  note:
    purpose: A short note.
    types:
      entry:
        fields:
          id: { type: string, required: true }
          state: { type: "enum(open, closed)", required: true }
    structure:
      entries: { type: list(entry), required: true, key: id, max: 50 }
      body: { type: text, required: true }
    checks:
      formal:
        CHECK
    uses:
      read:
        beneficiaries: "[]"
        requires_satisfied: [has-entries]
        requires:
          COND
`

func check(t *testing.T, formal, cond string) []string {
	t.Helper()
	src := strings.Replace(strings.Replace(pkgSource, "CHECK", formal, 1), "COND", cond, 1)
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return Check(map[string]*Package{"demo": Build("demo", "demo.yaml", content)})
}

const goodCheck = `has-entries: { rule: "Some entry.", expr: "size(self.entries) > 0", fix: "Add one." }`
const goodCond = `open: { rule: "An entry is open.", expr: "self.entries.exists(e, e.state == 'open')" }`

func TestCheckAcceptsAWellFormedPackage(t *testing.T) {
	if errs := check(t, goodCheck, goodCond); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestCheckReportsBadExpressions(t *testing.T) {
	for name, tc := range map[string]struct{ formal, cond, want string }{
		"undefined field":     {`has-entries: { rule: "x", expr: "size(self.entires) > 0", fix: "x" }`, goodCond, "undefined field 'entires'"},
		"not boolean":         {`has-entries: { rule: "x", expr: "size(self.entries)", fix: "x" }`, goodCond, "not bool"},
		"enum literal":        {goodCheck, `open: { rule: "x", expr: "self.entries.exists(e, e.state == 'opened')" }`, `"opened" is not a value of state`},
		"state in a check":    {`has-entries: { rule: "x", expr: "history(demo.note).size() > 0", fix: "x" }`, goodCond, "undeclared reference to 'history'"},
		"unknown capability":  {goodCheck, `open: { rule: "x", interpreter: verify.everything }`, "is not an interpreter capability"},
		"two forms":           {goodCheck, `open: { rule: "x", expr: "true", interpreter: verify.order }`, "exactly one of"},
		"unknown action":      {goodCheck, `open: { rule: "x", expr: "authorized(fact.signer, 'demo/note.uses.write')" }`, "is not a use"},
		"operation action":    {goodCheck, `open: { rule: "x", expr: "authorized(fact.signer, 'demo/note.uses.read')" }`, ""},
		"waivable":            {goodCheck, `open: { rule: "x", expr: "true", waivable: "yes" }`, "waivable: must be true or false"},
		"waivable capability": {goodCheck, `open: { rule: "x", interpreter: verify.order, waivable: true }`, "never waivable"},
		"no revises":          {goodCheck, `open: { rule: "x", expr: "predecessor.content().body == self.body" }`, "undeclared reference to 'predecessor'"},
		"cost over limit":     {`has-entries: { rule: "x", expr: "self.entries.all(a, self.entries.all(b, self.body.contains(self.body)))", fix: "x" }`, goodCond, "over the limit"},
		"let cycle":           {goodCheck, "open: { rule: \"x\", expr: \"true\" }\n        let:\n          a: \"b\"\n          b: \"a\"", "cycle"},
	} {
		errs := strings.Join(check(t, tc.formal, tc.cond), "\n")
		if tc.want == "" {
			if errs != "" {
				t.Errorf("%s: unexpected errors:\n%s", name, errs)
			}
			continue
		}
		if !strings.Contains(errs, tc.want) {
			t.Errorf("%s: want an error containing %q, got:\n%s", name, tc.want, errs)
		}
	}
}

func checkSource(t *testing.T, name, src string) string {
	t.Helper()
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return strings.Join(Check(map[string]*Package{name: Build(name, name+".yaml", content)}), "\n")
}

func TestCheckReportsBadShapes(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\n"
	note := "definitions:\n  note:\n    purpose: A note.\n    structure:\n      xs: { type: list(integer), required: true, max: 100000 }\n      body: { type: text, required: true }\n      tags: { type: list(text), required: true }\n"
	for name, tc := range map[string]struct{ src, want string }{
		"types as list":                        {head + "types: [a]\n" + note, "types: must be a mapping"},
		"validity in a package record":         {head + "types:\n  body:\n    fields:\n      validity: { type: time }\n" + note, "types.body.fields.validity: a validity inside an embedded record is never read"},
		"validity in a definition record":      {head + note + "    types:\n      body:\n        fields:\n          validity: { type: time }\n", "note.types.body.fields.validity: a validity inside an embedded record is never read"},
		"validity in a variant":                {head + "types:\n  body:\n    one_of:\n      validity: { type: time }\n" + note, "types.body.one_of.validity: a validity inside an embedded variant is never read"},
		"depends_on as string":                 {head + "depends_on: authority\n" + note, "depends_on: must be a list"},
		"requires as list":                     {head + note + "    uses:\n      read:\n        requires: [a]\n", "requires: must be a mapping"},
		"builtin name":                         {head + "definitions:\n  fact:\n    purpose: x\n", "fact is a built-in type name"},
		"type and definition":                  {head + "types:\n  note: { enum: [a] }\n" + note, "resolves twice"},
		"keyword field":                        {head + "definitions:\n  note:\n    purpose: x\n    structure:\n      in: { type: string }\n", "not a keyword"},
		"empty enum":                           {head + "types:\n  e: { enum: [] }\n" + note, "at least one value"},
		"repeated enum":                        {head + "types:\n  e: { enum: [a, a] }\n" + note, "listed twice"},
		"bad key":                              {head + "definitions:\n  note:\n    purpose: x\n    structure:\n      xs: { type: list(integer), key: id }\n", "must be an enabled scalar"},
		"repeated condition":                   {head + note + "    checks:\n      formal:\n        c: { rule: x, expr: \"true\", fix: x }\n    uses:\n      read:\n        requires_satisfied: [c]\n        requires:\n          c: { rule: x, expr: \"true\" }\n", "already names a condition c"},
		"let bound list":                       {head + note + "    checks:\n      formal:\n        c:\n          rule: x\n          let: { ys: \"self.xs.map(x, x)\" }\n          expr: \"ys.all(a, ys.all(b, a != b || a == b))\"\n          fix: x\n", "its worst case cost has no bound"},
		"flatten":                              {head + note + "    checks:\n      formal:\n        c: { rule: x, expr: \"self.xs.map(a, self.xs).flatten().all(p, self.xs.all(q, p != q || p == q))\", fix: x }\n", "over the limit"},
		"beneficiaries type":                   {head + note + "    uses:\n      read:\n        beneficiaries: \"self.body\"\n        requires:\n          c: { rule: x, expr: \"true\" }\n", "beneficiaries: is string, not list(string)"},
		"matches_name":                         {head + note + "    checks:\n      formal:\n        c: { rule: x, expr: \"self.tags.all(a, self.tags.all(b, matches_name(a, b)))\", fix: x }\n", "over the limit"},
		"revises":                              {head + note + "    uses:\n      read:\n        revises: \"yes\"\n        requires:\n          c: { rule: x, expr: \"true\" }\n", "revises: must be true or false"},
		"semantic uncombined":                  {head + note + "    checks:\n      semantic:\n        s: { expectation: x, evaluator: x }\n    uses:\n      read:\n        requires_satisfied: [s]\n", "declares no combination"},
		"combination rule":                     {head + note + "    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: most, cap: 3 } }\n", "rule: must be one of"},
		"combination cap":                      {head + note + "    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 0 } }\n", "cap: must be a positive integer"},
		"undeclared point":                     {head + note + "    operations:\n      land: { at: merge-path }\n", "must state at: one of verification, evaluation, write-path, client, or a decision point the package declares"},
		"redeclared point":                     {head + "decision_points:\n  client: { meaning: x }\n" + note, "client is a decision point the format defines"},
		"point of a non-dependency":            {head + note + "    operations:\n      land: { at: other/merge-path }\n", "names package other, which depends_on does not list"},
		"by":                                   {head + note + "    operations:\n      write: { at: client, by: anyone }\n", "unknown key by"},
		"no beneficiaries":                     {head + note + "    uses:\n      read:\n        requires:\n          c: { rule: x, expr: \"true\" }\n", "decides no fact, so it must name its beneficiaries"},
		"beneficiaries on a deciding use":      {head + note + "    uses:\n      recognize:\n        beneficiaries: \"[]\"\n", "decides facts, so it names no beneficiaries"},
		"independent on a deciding use":        {head + note + "    uses:\n      recognize:\n        independent: \"[]\"\n", "decides facts, so it names no independent decision"},
		"semantic check of a value":            {head + note + "    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 3 } }\n    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, check: note.checks.semantic.s, of: self }\n", "is semantic, so it is decided for a revision of the instance"},
		"semantic check through a use":         {head + note + "    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 3 } }\n    uses:\n      shared:\n        beneficiaries: \"[]\"\n        requires_satisfied: [s]\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, use: note.uses.shared, of: self }\n", "decides a semantic check, so it is decided for a revision of the instance"},
		"holds an unknown use of any":          {head + note + "      spec: { type: ref(any) }\n    uses:\n      recognize: {}\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"holds(self.spec, 'cuont')\" }\n", "holds names \"cuont\", which no package declares as a use"},
		"permitted an unknown use of any":      {head + note + "      spec: { type: ref(any) }\n    uses:\n      recognize: {}\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"permitted(self.spec, 'cuont')\" }\n", "permitted names \"cuont\", which no package declares as a use"},
		"permitted a use the definition lacks": {head + note + "      other: { type: ref(demo/note) }\n    uses:\n      recognize: {}\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"permitted(self.other, 'cuont')\" }\n", "permitted names \"cuont\", which is not a use of demo/note"},
		"permitted in a formal check":          {head + note + "    checks:\n      formal:\n        c: { rule: x, expr: \"permitted(self, 'read')\", fix: x }\n", "undeclared reference to 'permitted'"},
		"reliance requires nothing":            {head + note + "    uses:\n      read:\n        beneficiaries: \"[]\"\n", "decides no fact and requires nothing"},
		"governing":                            {head + "definitions:\n  note:\n    purpose: x\n    governing: \"yes\"\n", "governing: must be true or false"},
		"singleton":                            {head + "definitions:\n  note:\n    purpose: x\n    singleton: 1\n", "singleton: must be true or false"},
	} {
		if errs := checkSource(t, "demo", tc.src); !strings.Contains(errs, tc.want) {
			t.Errorf("%s: want an error containing %q, got:\n%s", name, tc.want, errs)
		}
	}
}

func TestCheckSurvivesTypesThatDoNotResolve(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: TYPE, required: true }\n    checks:\n      formal:\n        c: { rule: x, expr: \"EXPR\", fix: x }\n"
	for _, tc := range []struct{ typ, want string }{
		{`"list("`, "expected a type"},
		{`"map(string, "`, "expected a type"},
		{"nosuchtype", "type nosuchtype is not declared"},
		{"list(nosuchtype)", "type nosuchtype is not declared"},
		{"ref(nosuchtype)", "nosuchtype"},
	} {
		for _, expr := range []string{"self == self", "self != self", "[self] == [self]", "self in [self]", "self.body == self.body"} {
			src := strings.Replace(strings.Replace(head, "TYPE", tc.typ, 1), "EXPR", expr, 1)
			errs := checkSource(t, "demo", src)
			if !strings.Contains(errs, tc.want) {
				t.Errorf("%s with %s: want an error containing %q, got:\n%s", tc.typ, expr, tc.want, errs)
			}
			for _, bad := range []string{"internal error", "18446744073709551615", "over the limit", "no bound"} {
				if strings.Contains(errs, bad) {
					t.Errorf("%s with %s: the type's own error should stand alone, got:\n%s", tc.typ, expr, errs)
				}
			}
		}
	}
}

func TestCheckReportsAnUnboundedComparisonWithoutANumber(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ntypes:\n  node:\n    fields:\n      next: { type: list(node) }\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      head: { type: node, required: true }\n    checks:\n      formal:\n        c: { rule: x, expr: \"self.head == self.head\", fix: x }\n"
	errs := checkSource(t, "demo", src)
	if !strings.Contains(errs, "no bound") || strings.Contains(errs, "18446744073709551615") {
		t.Errorf("want an error saying the comparison has no bound, without the sentinel, got:\n%s", errs)
	}
}

func TestCheckAcceptsWhatTheFormatAllows(t *testing.T) {
	head := "format: 1\npackage: my-pkg\nrevision: 1\n"
	for name, src := range map[string]string{
		"hyphenated package":        head + "definitions:\n  note:\n    purpose: x\n    structure:\n      title: { type: string }\n    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"my_pkg.note{title: 'a'}.title == 'a'\" }\n",
		"lets that only look alike": head + "definitions:\n  note:\n    purpose: x\n    structure:\n      xs: { type: list(integer) }\n    uses:\n      read:\n        beneficiaries: \"[]\"\n        let:\n          a: \"self.xs.exists(b, b > 0)\"\n          b: \"self.xs.exists(a, a < 0)\"\n        requires:\n          c: { rule: x, expr: \"a || b\" }\n",
		"use keys":                  head + "definitions:\n  note:\n    purpose: x\n    structure:\n      authors: { type: list(principal) }\n    uses:\n      read:\n        candidate: \"'sha256:x'\"\n        beneficiaries: \"self.authors\"\n        accepts_declared: \"['my-pkg/note']\"\n        requires:\n          c: { rule: x, expr: \"true\", waivable: true }\n",
		"revisions":                 head + "definitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: text }\n    uses:\n      recognize:\n        revises: true\n        requires:\n          c: { rule: x, expr: \"fact.signer == predecessor.fact().signer\" }\n",
		"combined semantic check":   head + "definitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: text }\n    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 3 } }\n    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires_satisfied: [s]\n",
		"declared point":            head + "decision_points:\n  merge-path: { meaning: x }\ndefinitions:\n  note:\n    purpose: x\n    operations:\n      land: { at: merge-path, effect: x, recovery: x }\n      also: { at: my-pkg/merge-path }\n",
		"declarations":              head + "definitions:\n  note:\n    purpose: x\n    governing: true\n    singleton: true\n    uses:\n      recognize: {}\n",
		"of inside each":            head + "definitions:\n  note:\n    purpose: x\n    structure:\n      refs: { type: list(ref(note)) }\n    checks:\n      formal:\n        ok: { rule: x, expr: \"true\", fix: x }\n    uses:\n      recognize: {}\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, each: \"self.refs\", of: \"item.content()\", check: note.checks.formal.ok }\n",
	} {
		if errs := checkSource(t, "my-pkg", src); errs != "" {
			t.Errorf("%s: unexpected errors:\n%s", name, errs)
		}
	}
}

// An expression may name only the packages its package lists in depends_on, as a type or element
// path may.
func TestCheckRefusesAnExpressionNamingAnUnlistedPackage(t *testing.T) {
	demo := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  note:\n    purpose: A note.\n    structure:\n      body: { type: text, required: true }\n"
	other := func(depends string) string {
		return "format: 1\npackage: other\nrevision: 1\n" + depends + `definitions:
  probe:
    purpose: Reads another package's notes.
    structure:
      body: { type: text, required: true }
    uses:
      recognize:
        requires:
          reads-notes:
            rule: "Some recognized note exists."
            expr: "size(history(demo.note)) > 0"
`
	}
	build := func(src string) string {
		dc, err := Parse([]byte(demo))
		if err != nil {
			t.Fatal(err)
		}
		oc, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(Check(map[string]*Package{"demo": Build("demo", "demo.yaml", dc), "other": Build("other", "other.yaml", oc)}), "\n")
	}
	if errs := build(other("")); !strings.Contains(errs, "other.yaml: probe.uses.recognize.requires.reads-notes.expr: names package demo, which depends_on does not list") {
		t.Errorf("an unlisted package is accepted; the errors are:\n%s", errs)
	}
	if errs := build(other("depends_on: [demo]\n")); errs != "" {
		t.Errorf("a listed package is refused:\n%s", errs)
	}
	idle := strings.Replace(other("depends_on: [demo]\n"), "size(history(demo.note)) > 0", "true", 1)
	if errs := build(idle); !strings.Contains(errs, "other.yaml: depends_on: lists package demo, which nothing in the package names") {
		t.Errorf("a dependency nothing names is accepted; the errors are:\n%s", errs)
	}
}

// A string compared with a fact's kind names a definition as package/definition, so its package
// must be listed in depends_on, and the listing counts as used.
func TestCheckReadsAKindLiteralAsNamingItsPackage(t *testing.T) {
	demo := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  note:\n    purpose: A note.\n    structure:\n      body: { type: text, required: true }\n"
	other := func(depends, expr string) string {
		return "format: 1\npackage: other\nrevision: 1\n" + depends + `definitions:
  probe:
    purpose: Cites a record of some kind.
    structure:
      seen: { type: ref(any), required: true }
      keys: { type: "list(string)" }
    uses:
      recognize:
        requires:
          cites-a-note:
            rule: "The record it cites is not a note."
            expr: "` + expr + `"
`
	}
	build := func(src string) string {
		dc, err := Parse([]byte(demo))
		if err != nil {
			t.Fatal(err)
		}
		oc, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(Check(map[string]*Package{"demo": Build("demo", "demo.yaml", dc), "other": Build("other", "other.yaml", oc)}), "\n")
	}
	for _, c := range []struct{ name, depends, expr, want string }{
		{"unlisted", "", "self.seen.fact().kind == 'demo/note'", "names package demo, which depends_on does not list"},
		{"unlisted in a list", "", "self.seen.fact().kind in ['demo/note']", "names package demo, which depends_on does not list"},
		{"no such definition", "depends_on: [demo]\n", "self.seen.fact().kind in ['demo/note', 'demo/memo']", "demo/memo is not a definition"},
		{"no package", "depends_on: [demo]\n", "self.seen.fact().kind != 'note' && self.seen.fact().kind == 'demo/note'", `"note" is compared with a fact's kind but is not a kind as package/definition`},
		{"listed", "depends_on: [demo]\n", "'demo/note' == self.seen.fact().kind", ""},
		// A kind named as a string wherever it appears is a reference to a definition, whatever it is compared with.
		{"in a list of strings", "depends_on: [demo]\n", "self.keys.all(k, k == 'demo/memo')", "demo/memo is not a definition"},
		{"in a list of strings, unlisted", "", "self.keys.all(k, k == 'demo/note')", "names package demo, which depends_on does not list"},
		{"in a list of strings, listed", "depends_on: [demo]\n", "self.keys.all(k, k == 'demo/note')", ""},
		{"in a literal list", "depends_on: [demo]\n", "self.keys.exists(k, k in ['demo/note', 'demo/memo'])", "demo/memo is not a definition"},
		{"in a concatenation", "depends_on: [demo]\n", "self.keys.exists(k, k == 'demo/' + 'note') && self.seen.fact().kind == 'demo/memo'", "demo/memo is not a definition"},
		{"a string that is not a package's", "", "self.keys.exists(k, k == 'elsewhere/memo' || k == 'x/y/z' || k == 'demo/Note')", ""},
	} {
		errs := build(other(c.depends, c.expr))
		if c.want == "" && errs != "" {
			t.Errorf("%s: a kind of a listed package is refused:\n%s", c.name, errs)
		}
		if c.want != "" && !strings.Contains(errs, c.want) {
			t.Errorf("%s: want an error containing %q, got:\n%s", c.name, c.want, errs)
		}
	}
}

// The validity of a decision is read from its top level, so it is a field of the definition and
// never of a record the definition embeds.
func TestValidityAtTheTopLevelIsAllowed(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ntypes:\n  body:\n    fields:\n      reason: { type: text }\ndefinitions:\n  note:\n    purpose: A note.\n    structure:\n      body: { type: body, required: true }\n      validity: { type: time }\n"
	if errs := checkSource(t, "demo", src); errs != "" {
		t.Errorf("want no error, got:\n%s", errs)
	}
}

// A form embeds a shared header and applies the header's checks to it. A formal check of the
// header is decided on the header's value; a semantic check of it is never requested, because its
// findings are bound to a revision of the instance and not to a value inside it, and that holds
// whether the check lies in the embedded definition, whether the header is reached by of or by
// each, and when the embedded value is the content of a reference.
func TestCheckRefusesASemanticCheckOfAnEmbeddedValue(t *testing.T) {
	core := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  header:\n    purpose: What every form shares.\n    structure:\n      source: { type: text }\n      unknown: { type: boolean, required: true }\n    checks:\n      formal:\n        explicit: { rule: The source is named or stated unknown., expr: \"self.unknown || has(self.source)\", fix: Name it. }\n      semantic:\n        kinds: { expectation: Kinds are kept apart., evaluator: any, combination: { rule: unanimous, cap: 1 } }\n    uses:\n      recognize: {}\n"
	form := func(cond string) string {
		return core + "  report:\n    purpose: A report.\n    structure:\n      header: { type: header, required: true }\n      headers: { type: list(header) }\n      other: { type: ref(header) }\n    uses:\n      recognize:\n        requires:\n          c: " + cond + "\n"
	}
	for name, tc := range map[string]struct{ cond, want string }{
		"of the embedded header":  {"{ rule: x, check: demo/header.checks.semantic.kinds, of: self.header }", "is semantic, so it is decided for a revision of the instance"},
		"of each embedded header": {"{ rule: x, each: self.headers, of: item, check: demo/header.checks.semantic.kinds }", "is semantic, so it is decided for a revision of the instance"},
		"of a referenced content": {"{ rule: x, check: demo/header.checks.semantic.kinds, of: self.other.content() }", "is semantic, so it is decided for a revision of the instance"},
		"without of, wrong type":  {"{ rule: x, check: demo/header.checks.semantic.kinds }", "applies to"},
		"formal check, no value":  {"{ rule: x, check: demo/header.checks.formal.explicit }", "applies to"},
	} {
		errs := checkSource(t, "demo", form(tc.cond))
		if !strings.Contains(errs, tc.want) {
			t.Errorf("%s: want an error containing %q, got:\n%s", name, tc.want, errs)
		}
	}
	if errs := checkSource(t, "demo", form("{ rule: x, check: header.checks.formal.explicit, of: self.header }")); errs != "" {
		t.Errorf("a formal check of the embedded header is refused:\n%s", errs)
	}
}

// A condition that requires_satisfied stands for is addressable as an element path, like one
// written under requires.
func TestElementResolvesRequiresSatisfied(t *testing.T) {
	src := strings.Replace(strings.Replace(pkgSource, "CHECK", goodCheck, 1), "COND", goodCond, 1)
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u, problems := Prepare(map[string]*Package{"demo": Build("demo", "demo.yaml", content)})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for path, isCondition := range map[string]bool{
		"note.uses.read.requires.has-entries": true,
		"note.uses.read.requires.open":        true,
		"note.uses.read.requires.missing":     false,
		"note.uses.read.requires_satisfied":   false,
	} {
		_, parts, node, err := u.Element(u.Packages["demo"], path, false)
		if got := err == nil && Shape(parts) == "condition"; got != isCondition {
			t.Errorf("%s: a condition is %v, want %v (%v)", path, got, isCondition, err)
		}
		if path == "note.uses.read.requires.has-entries" && err == nil && mapping(node)["check"] != "note.checks.formal.has-entries" {
			t.Errorf("%s: the condition is %v, not a check of has-entries", path, node)
		}
	}
}

// combination_of reads the combination a semantic check declares as a constant fixed when the
// package is read, so its argument must be a string literal naming such a check.
func TestCombinationOf(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\n"
	other := "format: 1\npackage: other\nrevision: 1\ndefinitions:\n  thing:\n    purpose: x\n    checks:\n      semantic:\n        judged: { expectation: x, evaluator: x, combination: { rule: majority, cap: 5 } }\n"
	value := func(cond string) string {
		return head + "depends_on: [other]\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      name: { type: string }\n    checks:\n      formal:\n        shape: { rule: x, expr: \"true\", fix: x }\n      semantic:\n        judged: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 3 } }\n        open: { expectation: x, evaluator: x }\n    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"" + cond + "\" }\n          d: { rule: x, expr: \"combination_of('other/thing.checks.semantic.judged').cap == 5\" }\n"
	}
	packages := func(src string) map[string]*Package {
		parse := func(name, text string) *Package {
			content, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, text)
			}
			return Build(name, name+".yaml", content)
		}
		return map[string]*Package{"demo": parse("demo", src), "other": parse("other", other)}
	}
	for name, tc := range map[string]struct{ cond, want string }{
		"a declared combination":     {"combination_of('demo/note.checks.semantic.judged').cap == 3", ""},
		"of another package":         {"combination_of('other/thing.checks.semantic.judged').rule == 'majority'", ""},
		"a formal check":             {"combination_of('demo/note.checks.formal.shape').cap == 3", "is not a semantic check"},
		"a check that declares none": {"combination_of('demo/note.checks.semantic.open').cap == 3", "declares no combination"},
		"not a check":                {"combination_of('demo/note.uses.read').cap == 3", "is not a semantic check"},
		"no such check":              {"combination_of('demo/note.checks.semantic.nope').cap == 3", "does not exist"},
		"not qualified":              {"combination_of('note.checks.semantic.judged').cap == 3", "qualified by package"},
		"not a literal":              {"combination_of(self.name).cap == 3", "string literal"},
		"a package not listed":       {"combination_of('elsewhere/thing.checks.semantic.judged').cap == 3", "elsewhere"},
	} {
		got := strings.Join(Check(packages(value(tc.cond))), "\n")
		switch {
		case tc.want == "" && got != "":
			t.Errorf("%s: unexpected errors:\n%s", name, got)
		case tc.want != "" && !strings.Contains(got, tc.want):
			t.Errorf("%s: want %q, got:\n%s", name, tc.want, got)
		}
	}

	u, problems := Prepare(packages(value("true")))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for expr, want := range map[string]string{
		"combination_of('demo/note.checks.semantic.judged') == strux.combination{rule: 'unanimous', cap: 3}":  "true",
		"combination_of('demo/note.checks.semantic.judged').cap == 3":                                         "true",
		"combination_of('other/thing.checks.semantic.judged') == strux.combination{rule: 'majority', cap: 5}": "true",
		"combine([], combination_of('demo/note.checks.semantic.judged'))":                                     "unknown",
	} {
		got, problems := u.EvaluateCheck("demo", "note", nil, expr, map[string]any{})
		if len(problems) > 0 {
			t.Errorf("%s: %v", expr, problems)
		} else if got.Value != want {
			t.Errorf("%s: got %s (%s), want %s", expr, got.Value, got.Reason, want)
		}
	}
}

// A list may be keyed by a field of a declared enumeration as well as by a scalar or an inline
// enumeration; a field of a declared record, or of an unresolved type, is still refused.
func TestKeyMayBeADeclaredEnumeration(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\n"
	other := "format: 1\npackage: other\nrevision: 1\ntypes:\n  phase: { enum: [first, second] }\n  slot:\n    fields:\n      phase: { type: phase, required: true }\ndefinitions:\n  thing:\n    purpose: x\n"
	use := func(src string) string {
		return head + src
	}
	named := func(src string) string {
		return head + "depends_on: [other]\n" + src
	}
	packages := func(src string) map[string]*Package {
		parse := func(name, text string) *Package {
			content, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, text)
			}
			return Build(name, name+".yaml", content)
		}
		return map[string]*Package{"demo": parse("demo", src), "other": parse("other", other)}
	}
	for name, tc := range map[string]struct{ src, want string }{
		"a package enumeration":                             {use("types:\n  kind: { enum: [a, b] }\n  row:\n    fields:\n      kind: { type: kind, required: true }\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      rows: { type: list(row), key: kind }\n"), ""},
		"a definition enumeration":                          {use("definitions:\n  note:\n    purpose: x\n    types:\n      kind: { enum: [a, b] }\n      row:\n        fields:\n          kind: { type: kind, required: true }\n    structure:\n      rows: { type: list(row), key: kind }\n"), ""},
		"an enumeration of a dependency, in its own record": {named("definitions:\n  note:\n    purpose: x\n    structure:\n      rows: { type: list(other/slot), key: phase }\n"), ""},
		"a record":  {use("types:\n  inner:\n    fields:\n      x: { type: string }\n  row:\n    fields:\n      inner: { type: inner, required: true }\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      rows: { type: list(row), key: inner }\n"), "must be an enabled scalar or enumerated field"},
		"a variant": {use("types:\n  choice:\n    one_of:\n      a: { type: string }\n  row:\n    fields:\n      choice: { type: choice, required: true }\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      rows: { type: list(row), key: choice }\n"), "must be an enabled scalar or enumerated field"},
	} {
		got := strings.Join(Check(packages(tc.src)), "\n")
		switch {
		case tc.want == "" && got != "":
			t.Errorf("%s: unexpected errors:\n%s", name, got)
		case tc.want != "" && !strings.Contains(got, tc.want):
			t.Errorf("%s: want %q, got:\n%s", name, tc.want, got)
		}
	}
}

// candidate_findings is declared in a use and refused in a formal check, and names a check as
// findings does.
func TestCandidateFindings(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: text }\n    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 1 } }\n      formal:\n        f: { rule: x, expr: \"true\", fix: x }\n"
	use := func(cond string) string {
		return head + "    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"" + cond + "\" }\n"
	}
	for name, tc := range map[string]struct{ src, want string }{
		"in a use":          {use("combine(candidate_findings('demo/note.checks.semantic.s', self.body), strux.combination{rule: 'unanimous', cap: 1})"), ""},
		"not a check":       {use("size(candidate_findings('demo/note.uses.read', 'sha256:x')) > 0"), "candidate_findings names"},
		"not qualified":     {use("size(candidate_findings('note.checks.semantic.s', 'sha256:x')) > 0"), "candidate_findings names"},
		"in a formal check": {head + "        g: { rule: x, expr: \"size(candidate_findings('demo/note.checks.semantic.s', 'sha256:x')) > 0\", fix: x }\n", "undeclared reference to 'candidate_findings'"},
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

// A reference is to a fact, and a fact is recognized only under a use: its kind's `recognize` use, or the use
// an operation of its own kind names, since every fact is decided by one use. A reference to a kind with neither could
// never be satisfied, and a binder's operation that fills an abstract operation adds no use to the
// kind that declares it.
func TestCheckRefusesAReferenceToAKindNoFactOfWhichIsRecognized(t *testing.T) {
	head := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  shot:\n    purpose: A captured file.\n    structure:\n      name: { type: string }\n"
	holder := func(shot string) string {
		return head + shot + "  report:\n    purpose: A report.\n    structure:\n      files: { type: list(ref(shot)) }\n      one: { type: ref(shot | report) }\n    uses:\n      recognize: {}\n"
	}
	for name, tc := range map[string]struct{ shot, want string }{
		"no use at all":              {"", "ref(shot) can never be satisfied"},
		"an abstract operation":      {"    operations:\n      capture: { at: verification, abstract: true }\n", "ref(shot) can never be satisfied"},
		"a use other than recognize": {"    uses:\n      read:\n        beneficiaries: \"[]\"\n", "ref(shot) can never be satisfied"},
		"an operation that decides":  {"    uses:\n      gate:\n        beneficiaries: \"[]\"\n    operations:\n      check: { at: verification, effect: x, decides: gate }\n", "ref(shot) can never be satisfied"},
	} {
		errs := checkSource(t, "demo", holder(tc.shot))
		if !strings.Contains(errs, tc.want) {
			t.Errorf("%s: want an error containing %q, got:\n%s", name, tc.want, errs)
		}
	}
	for name, shot := range map[string]string{
		"a recognize use":        "    uses:\n      recognize: {}\n",
		"an operation that uses": "    uses:\n      record:\n        requires: {}\n    operations:\n      add: { at: verification, effect: x, recovery: x, use: record }\n",
	} {
		if errs := checkSource(t, "demo", holder(shot)); errs != "" {
			t.Errorf("%s: a decidable kind is refused:\n%s", name, errs)
		}
	}
}
