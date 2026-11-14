package format

import (
	"strings"
	"testing"
)

const partsHead = "format: 1\npackage: demo\nrevision: 1\n"

// noteWith is a package whose definition has two uses, and extra in place of its parts and uses.
func noteWith(parts, uses string) string {
	return partsHead + parts + "definitions:\n  note:\n    purpose: A note.\n    structure:\n      body: { type: text }\n      owner: { type: principal }\n    checks:\n      formal:\n        titled: { rule: x, expr: \"size(self.body) > 0\", fix: x }\n    uses:\n" + uses
}

func parsed(t *testing.T, src string) map[string]any {
	t.Helper()
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return content.(map[string]any)
}

func buildSource(t *testing.T, src string) *Package {
	t.Helper()
	return Build("demo", "demo.yaml", parsed(t, src))
}

// A part is a template of lets and conditions that a use includes. The package is read as if the
// use had written them, so a package with parts and the same package written inline have one
// identity and one result.
func TestPartsExpandIntoTheCanonicalForm(t *testing.T) {
	withParts := noteWith(
		"parts:\n  shared:\n    let:\n      size_of_body: \"size(self.body)\"\n    conditions:\n      has-body: { rule: The note has a body., expr: \"size_of_body > 0\" }\n      owned: { rule: The note has an owner., expr: \"self.owner != ''\", waivable: true }\n",
		"      read:\n        beneficiaries: \"[]\"\n        includes: [shared]\n        requires:\n          titled: { rule: The note is titled., check: note.checks.formal.titled }\n      write:\n        beneficiaries: \"[]\"\n        let:\n          other: \"1\"\n        includes: [shared]\n")
	inline := noteWith("",
		"      read:\n        beneficiaries: \"[]\"\n        let:\n          size_of_body: \"size(self.body)\"\n        requires:\n          titled: { rule: The note is titled., check: note.checks.formal.titled }\n          has-body: { rule: The note has a body., expr: \"size_of_body > 0\" }\n          owned: { rule: The note has an owner., expr: \"self.owner != ''\", waivable: true }\n      write:\n        beneficiaries: \"[]\"\n        let:\n          other: \"1\"\n          size_of_body: \"size(self.body)\"\n        requires:\n          has-body: { rule: The note has a body., expr: \"size_of_body > 0\" }\n          owned: { rule: The note has an owner., expr: \"self.owner != ''\", waivable: true }\n")
	a, b := buildSource(t, withParts), buildSource(t, inline)
	if errs := Check(map[string]*Package{"demo": a}); len(errs) > 0 {
		t.Fatalf("the package with parts: %v", errs)
	}
	if errs := Check(map[string]*Package{"demo": b}); len(errs) > 0 {
		t.Fatalf("the inline package: %v", errs)
	}
	if a.Digest != b.Digest {
		t.Errorf("the digest of the package with parts is %s, and of the inline package %s", a.Digest, b.Digest)
	}
	if got, want := Digest(Expanded(parsed(t, withParts))), b.Digest; got != want {
		t.Errorf("Expanded gives a form with digest %s, want %s", got, want)
	}
	if Digest(parsed(t, withParts)) == a.Digest {
		t.Errorf("the digest of the file as written is the identity, but parts must expand first")
	}
	// An included condition has the element path of one written in the use.
	u, problems := Prepare(map[string]*Package{"demo": a})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for _, path := range []string{"note.uses.read.requires.has-body", "note.uses.write.requires.owned", "note.uses.read.requires.titled"} {
		_, parts, node, err := u.Element(a, path, false)
		if err != nil || Shape(parts) != "condition" || node == nil {
			t.Errorf("%s: shape %q, %v", path, Shape(parts), err)
		}
	}
	// What the file as written holds is not changed by reading it.
	if _, ok := parsed(t, withParts)["parts"]; !ok {
		t.Fatal("the test source lost its parts")
	}
	content := parsed(t, withParts)
	Expanded(content)
	Build("demo", "demo.yaml", content)
	if _, ok := content["parts"]; !ok {
		t.Errorf("reading the package removed the parts from the content it was given")
	}
	if uses := mapping(mapping(mapping(content["definitions"])["note"])["uses"]); mapping(uses["read"])["includes"] == nil {
		t.Errorf("reading the package removed includes from the content it was given")
	}
}

func TestPartsAreRefusedWhereTheyAreWrong(t *testing.T) {
	shared := "parts:\n  shared:\n    conditions:\n      has-body: { rule: x, expr: \"size(self.body) > 0\" }\n"
	use := func(body string) string {
		return "      read:\n        beneficiaries: \"[]\"\n" + body
	}
	for name, tc := range map[string]struct{ src, want string }{
		"a part nothing includes":         {noteWith(shared, use("        requires:\n          c: { rule: x, expr: \"true\" }\n")), "part shared is included by no use"},
		"an unknown part":                 {noteWith(shared, use("        includes: [shared, nowhere]\n")), "includes nowhere, which is no part of the package"},
		"a part listed twice":             {noteWith(shared, use("        includes: [shared, shared]\n")), "includes shared twice"},
		"includes is not a list":          {noteWith(shared, use("        includes: shared\n")), "includes: must be a list of part names"},
		"includes holds a non-name":       {noteWith(shared, use("        includes: [1]\n")), "includes: must be a list of part names"},
		"a condition taken twice":         {noteWith(shared, use("        includes: [shared]\n        requires:\n          has-body: { rule: x, expr: \"true\" }\n")), "condition has-body is taken twice, by the use and by part shared"},
		"two parts take a condition":      {noteWith(shared+"  other:\n    conditions:\n      has-body: { rule: x, expr: \"true\" }\n", use("        includes: [shared, other]\n")), "condition has-body is taken twice, by part shared and by part other"},
		"a condition taken by a check":    {noteWith("parts:\n  clash:\n    conditions:\n      titled: { rule: x, expr: \"true\" }\n", use("        includes: [clash]\n        requires_satisfied: [titled]\n")), "condition titled is taken twice, by the use and by part clash"},
		"includes lists no part":          {noteWith(shared, use("        includes: []\n")), "includes: must be a list of part names, and not empty"},
		"a let taken twice":               {noteWith("parts:\n  lets:\n    let:\n      limit: \"1\"\n    conditions:\n      c: { rule: x, expr: \"limit == 1\" }\n", use("        includes: [lets]\n        let:\n          limit: \"2\"\n")), "let limit is taken twice, by the use and by part lets"},
		"a part of nothing":               {noteWith("parts:\n  empty: {}\n", use("        includes: [empty]\n        requires:\n          c: { rule: x, expr: \"true\" }\n")), "part empty has neither let nor conditions"},
		"a part with another key":         {noteWith("parts:\n  odd:\n    purpose: x\n    conditions:\n      c: { rule: x, expr: \"true\" }\n", use("        includes: [odd]\n")), "parts.odd: unknown key purpose"},
		"a part that is not a mapping":    {noteWith("parts:\n  odd: 1\n", use("        requires:\n          c: { rule: x, expr: \"true\" }\n")), "parts.odd: must be a mapping"},
		"a part name":                     {noteWith("parts:\n  Odd_Name:\n    conditions:\n      c: { rule: x, expr: \"true\" }\n", use("        includes: [Odd_Name]\n")), "a name is lowercase words joined by hyphens"},
		"parts is not a mapping":          {noteWith("parts: [a]\n", use("        requires:\n          c: { rule: x, expr: \"true\" }\n")), "parts: must be a mapping"},
		"a part is not a use":             {noteWith(shared, use("        includes: [shared]\n        requires:\n          c: { rule: x, expr: \"authorized(fact.signer, 'demo/note.uses.shared')\" }\n")), "is not a use"},
		"a part in a part":                {noteWith("parts:\n  outer:\n    includes: [inner]\n    conditions:\n      c: { rule: x, expr: \"true\" }\n", use("        includes: [outer]\n")), "parts.outer: unknown key includes"},
		"let of a use that includes":      {noteWith("parts:\n  lets:\n    let:\n      limit: \"1\"\n    conditions:\n      c: { rule: x, expr: \"limit == 1\" }\n", use("        includes: [lets]\n        let: bogus\n")), "uses.read.let: must be a mapping"},
		"requires of a use that includes": {noteWith(shared, use("        includes: [shared]\n        requires: [a]\n")), "uses.read.requires: must be a mapping"},
		"includes in a check":             {noteWith(shared, use("        includes: [shared]\n")+"    operations:\n      write: { at: client, includes: [shared] }\n"), "unknown key includes"},
	} {
		got := checkSource(t, "demo", tc.src)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: want %q, got:\n%s", name, tc.want, got)
		}
	}
}

// A part is type-checked where it is included, in the context of that use.
func TestAPartIsTypeCheckedAtEachIncludingUse(t *testing.T) {
	part := "parts:\n  moved:\n    conditions:\n      same-body: { rule: x, expr: \"predecessor.content().body == self.body\" }\n"
	uses := "      recognize:\n        revises: true\n        includes: [moved]\n      read:\n        beneficiaries: \"[]\"\n        includes: [moved]\n"
	got := checkSource(t, "demo", noteWith(part, uses))
	if !strings.Contains(got, "note.uses.read.requires.same-body.expr") || !strings.Contains(got, "undeclared reference to 'predecessor'") {
		t.Errorf("the use that does not revise should report the part, got:\n%s", got)
	}
	if strings.Contains(got, "note.uses.recognize") {
		t.Errorf("the use that revises should accept the part, got:\n%s", got)
	}
}
