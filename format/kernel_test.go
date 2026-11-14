package format

import (
	"strings"
	"testing"
)

// The kernel's name is given to the universe, never assumed: the format reads the name of the
// package whose finding type, custody profile and trust root it knows from the one that is named.
func TestKernelNameIsGiven(t *testing.T) {
	kernel := "format: 1\npackage: judge\nrevision: 1\ndefinitions:\n" +
		"  finding:\n    purpose: x\n    structure:\n      verdict: { type: string }\n" +
		"  custody-profile:\n    purpose: x\n    structure:\n      evaluator: { type: string }\n" +
		"  trust-root:\n    purpose: x\n    structure:\n      owner: { type: string }\n"
	app := func(cond string) string {
		return "format: 1\npackage: app\nrevision: 1\ndepends_on: [judge]\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: text }\n" +
			"    checks:\n      semantic:\n        s: { expectation: x, evaluator: x, combination: { rule: unanimous, cap: 1 } }\n" +
			"    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"" + cond + "\" }\n"
	}
	build := func(cond string, opts ...Option) string {
		pkgs := map[string]*Package{}
		for name, text := range map[string]string{"judge": kernel, "app": app(cond)} {
			content, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			pkgs[name] = Build(name, name+".yaml", content)
		}
		return strings.Join(Check(pkgs, opts...), "\n")
	}
	findings := "candidate_findings('app/note.checks.semantic.s', self.body).all(r, r.content().verdict == 'ok')"
	custody := "custody.evaluator == 'x' && trust_root.owner == 'y'"
	for name, tc := range map[string]struct {
		cond string
		opts []Option
		want string
	}{
		"findings of the named kernel":   {findings, []Option{Kernel("judge")}, ""},
		"custody of the named kernel":    {custody, []Option{Kernel("judge")}, ""},
		"findings, no kernel named":      {findings, nil, "unexpected failed resolution of"},
		"custody, no kernel named":       {custody, nil, "undeclared reference to 'custody'"},
		"findings, another kernel named": {findings, []Option{Kernel("other")}, "unexpected failed resolution of"},
		"the old name is no kernel":      {custody, []Option{Kernel("strux")}, "undeclared reference to 'custody'"},
		// The kernel is exempt from the rule that a dependency must be named.
		"kernel dependency unused":     {"true", []Option{Kernel("judge")}, ""},
		"dependency unused, no kernel": {"true", nil, "lists package judge, which nothing in the package names"},
	} {
		got := build(tc.cond, tc.opts...)
		switch {
		case tc.want == "" && got != "":
			t.Errorf("%s: unexpected errors:\n%s", name, got)
		case tc.want != "" && !strings.Contains(got, tc.want):
			t.Errorf("%s: want %q, got:\n%s", name, tc.want, got)
		}
	}
}

// A kernel that lacks a definition the format reads its context variables from is reported once,
// as the cause, and the expressions that read the variables are not reported for an undeclared name.
func TestRenamedContextDefinitionIsOneCause(t *testing.T) {
	kernel := "format: 1\npackage: judge\nrevision: 1\ndefinitions:\n" +
		"  finding:\n    purpose: x\n    structure:\n      verdict: { type: string }\n" +
		"  custody-profile:\n    purpose: x\n    structure:\n      evaluator: { type: string }\n" +
		"  keys:\n    purpose: x\n    structure:\n      owner: { type: string }\n"
	app := "format: 1\npackage: app\nrevision: 1\ndepends_on: [judge]\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: text }\n" +
		"    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"custody.evaluator == 'x' && trust_root.owner == 'y'\" }\n"
	pkgs := map[string]*Package{}
	for name, text := range map[string]string{"judge": kernel, "app": app} {
		content, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		pkgs[name] = Build(name, name+".yaml", content)
	}
	got := strings.Join(Check(pkgs, Kernel("judge")), "\n")
	if !strings.Contains(got, "derives trust_root from the definition trust-root of the kernel package") {
		t.Errorf("the missing definition is not reported as the cause:\n%s", got)
	}
	if strings.Contains(got, "undeclared reference") || strings.Contains(got, "derives custody") {
		t.Errorf("a symptom is reported beside the cause, or a definition that exists is reported:\n%s", got)
	}
}

// The built-in records keep one namespace of the format's own, whatever the kernel is called, and
// no package may take it as a name in expressions.
func TestBuiltinNamespaceBelongsToTheFormat(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  note:\n    purpose: x\n    structure:\n      num: { type: integer }\n" +
		"    checks:\n      formal:\n        f: { rule: x, expr: \"strux.combination{rule: 'unanimous', cap: 1}.cap == 1\", fix: x }\n"
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, kernel := range []string{"", "judge", "kernel"} {
		if got := Check(map[string]*Package{"demo": Build("demo", "demo.yaml", content)}, Kernel(kernel)); len(got) > 0 {
			t.Errorf("kernel %q: the built-in record is refused: %v", kernel, got)
		}
	}
}

// The lets and conditions of a chain act's uses read only self, fact and trust_root, and call no
// function that reads the log, because verification judges chain acts before the anchors, any
// configuration or a grant. A use of any other definition, of the kernel or not, reads as usual.
func TestChainActsReadOnlySelfFactAndTrustRoot(t *testing.T) {
	kernel := func(trustRoot, revocation, note string) string {
		return "format: 1\npackage: judge\nrevision: 1\ndefinitions:\n" +
			"  finding:\n    purpose: x\n    structure:\n      verdict: { type: string }\n    uses:\n      recognize: {}\n" +
			"  custody-profile:\n    purpose: x\n    structure:\n      evaluator: { type: string }\n" +
			"  trust-root:\n    purpose: x\n    structure:\n      owner: { type: string }\n      log: { type: digest }\n" +
			"    uses:\n      recognize:\n        revises: true\n" + trustRoot +
			"  compromise-revocation:\n    purpose: x\n    structure:\n      credential: { type: string }\n      reason: { type: text }\n" +
			"    uses:\n      recognize:\n" + revocation +
			"  note:\n    purpose: x\n    structure:\n      body: { type: text }\n    uses:\n      recognize:\n" + note
	}
	cond := func(expr string) string {
		return "        requires:\n          c: { rule: x, expr: \"" + expr + "\" }\n"
	}
	own := cond("self.log == trust_root.log && fact.signer == trust_root.owner")
	ownRevocation := cond("self.credential != trust_root.owner && size(self.reason) > 0 && has(fact.expected) == false")
	for name, tc := range map[string]struct {
		trustRoot, revocation, note string
		want                        string // the one error, or none
	}{
		"self, fact and trust_root":         {own, ownRevocation, cond("true"), ""},
		"custody":                           {cond("custody.evaluator != trust_root.owner"), ownRevocation, cond("true"), "trust-root.uses.recognize.requires.c.expr: reads custody, but a chain act"},
		"predecessor and content":           {cond("predecessor.content().owner == trust_root.owner"), ownRevocation, cond("true"), "trust-root.uses.recognize.requires.c.expr: reads predecessor, content(), but a chain act"},
		"history":                           {own, cond("history(judge.finding).exists(f, f.digest() != '')"), cond("true"), "compromise-revocation.uses.recognize.requires.c.expr: reads history(), but a chain act"},
		"a let":                             {own, "        let:\n          e: \"custody.evaluator\"\n" + cond("e == self.credential"), cond("true"), "compromise-revocation.uses.recognize.let.e: reads custody, but a chain act"},
		"a let it reads is no variable":     {own, "        let:\n          e: \"self.credential\"\n" + cond("e != trust_root.owner"), cond("true"), ""},
		"another definition reads as usual": {own, ownRevocation, cond("custody.evaluator != fact.signer && history(judge.finding).exists(f, f.content().verdict == 'ok')"), ""},
	} {
		content, err := Parse([]byte(kernel(tc.trustRoot, tc.revocation, tc.note)))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		errs := Check(map[string]*Package{"judge": Build("judge", "judge.yaml", content)}, Kernel("judge"))
		switch {
		case tc.want == "" && len(errs) > 0:
			t.Errorf("%s: unexpected errors:\n%s", name, strings.Join(errs, "\n"))
		case tc.want != "" && (len(errs) != 1 || !strings.Contains(errs[0], tc.want)):
			t.Errorf("%s: want one error with %q, got:\n%s", name, tc.want, strings.Join(errs, "\n"))
		}
	}
}
