package format

import (
	"strings"
	"testing"
)

// A decision point named in an expression is a key of the custody profile's decision points, so a
// literal that indexes or tests membership in them must name a point the format defines or one a
// package declares, and a definition's name is not a point.
func TestDecisionPointLiteralsNameDeclaredPoints(t *testing.T) {
	kernel := "format: 1\npackage: judge\nrevision: 1\ndefinitions:\n" +
		"  custody-profile:\n    purpose: x\n    structure:\n      decision_points: { type: \"map(string, string)\" }\n" +
		"  trust-root:\n    purpose: x\n    structure:\n      owner: { type: string }\n"
	app := func(cond string) string {
		return "format: 1\npackage: app\nrevision: 1\ndepends_on: [judge]\ndecision_points:\n  gate: { meaning: x }\n" +
			"definitions:\n  note:\n    purpose: x\n    structure:\n      body: { type: text }\n" +
			"    uses:\n      read:\n        beneficiaries: \"[]\"\n        requires:\n          c: { rule: x, expr: \"" + cond + "\" }\n"
	}
	build := func(cond string) string {
		pkgs := map[string]*Package{}
		for name, text := range map[string]string{"judge": kernel, "app": app(cond)} {
			content, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			pkgs[name] = Build(name, name+".yaml", content)
		}
		return strings.Join(Check(pkgs, Kernel("judge")), "\n")
	}
	for name, tc := range map[string]struct{ cond, want string }{
		"a declared point":                  {"'app/gate' in custody.decision_points", ""},
		"a point the format defines":        {"'verification' in custody.decision_points", ""},
		"an indexed declared point":         {"'app/gate' in custody.decision_points && custody.decision_points['app/gate'] == 'x'", ""},
		"a point nothing declares":          {"'app/gatex' in custody.decision_points", "app/gatex is neither a decision point the format defines nor one app declares"},
		"an indexed point nothing declares": {"custody.decision_points['app/gatex'] == 'x'", "app/gatex is neither a decision point the format defines nor one app declares"},
		"a definition is no point":          {"'app/note' in custody.decision_points", "app/note is neither a decision point the format defines nor one app declares"},
		"a name computed, left alone":       {"('app/' + 'gatex') in custody.decision_points", ""},
		"another map, left alone":           {"'app/gatex' in {'app/gatex': 1}", "app/gatex is not a definition or a decision point"},
	} {
		got := build(tc.cond)
		switch {
		case tc.want == "" && got != "":
			t.Errorf("%s: unexpected errors:\n%s", name, got)
		case tc.want != "" && !strings.Contains(got, tc.want):
			t.Errorf("%s: want %q, got:\n%s", name, tc.want, got)
		}
	}
}
