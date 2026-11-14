package format

import "testing"

// A list read from the log lists a revision whose fact is pending as a possible member. The
// reference interpreter reads such a list as the format's evaluation says, and every plain list
// behaves as it did.
func TestPossibleMembers(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  value:\n    purpose: x\n    types:\n      entry:\n        fields:\n          label: { type: string }\n    structure:\n      entries: { type: list(entry) }\n      more: { type: list(entry) }\n      words: { type: list(string) }\n      tags: { type: \"map(string, string)\" }\n      findings: { type: list(integer) }\n"
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u, problems := Prepare(map[string]*Package{"demo": Build("demo", "demo.yaml", content)})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	entry := func(label string) any { return map[string]any{"label": label} }
	self := map[string]any{
		"entries": []any{entry("a"), entry("b"), entry("c")},
		"more":    []any{entry("d")},
		"words":   []any{"a", "b", "c"},
		"tags":    map[string]any{"k": "v"},
	}
	possible := map[string][]int{"entries": {1}}
	for _, tc := range []struct {
		expr, want string
		possible   map[string][]int
	}{
		// exists: true when a certain member satisfies, false when no item does, otherwise unknown
		{"self.entries.exists(e, e.label == 'a')", "true", possible},
		{"self.entries.exists(e, e.label == 'b')", "unknown", possible},
		{"self.entries.exists(e, e.label == 'z')", "false", possible},
		{"self.entries.exists(e, e.label == 'b' || e.label == 'c')", "true", possible},
		// all: false when a certain member fails, true when every item satisfies, otherwise unknown
		{"self.entries.all(e, e.label != 'a')", "false", possible},
		{"self.entries.all(e, e.label != 'b')", "unknown", possible},
		{"self.entries.all(e, size(e.label) == 1)", "true", possible},
		{"self.entries.all(e, e.label != 'b' && e.label != 'a')", "false", possible},
		// membership is exists with equality
		{"'a' in self.words", "true", possible},
		{"self.entries.map(e, e.label).exists(l, l == 'b')", "unknown", possible},
		// filter drops an item the predicate is false for
		{"size(self.entries.filter(e, e.label != 'b')) == 2", "true", possible},
		{"size(self.entries.filter(e, e.label != 'a')) == 2", "unknown", possible},
		{"self.entries.filter(e, e.label != 'a').exists(e, e.label == 'c')", "true", possible},
		{"self.entries.filter(e, e.label != 'a').exists(e, e.label == 'b')", "unknown", possible},
		{"self.entries.filter(e, e.label != 'a').all(e, e.label != 'a')", "true", possible},
		// a filter that a certain member's unknown result spoils is unknown, as for a plain list
		{"size(self.entries.filter(e, e.label == self.entries[7].label)) == 0", "unknown", nil},
		// a possible member's unknown predicate leaves it possible
		{"size(self.entries.filter(e, e.label == 'b' ? self.entries[7].label == 'x' : false)) == 0", "unknown", possible},
		// map keeps each item's status
		{"self.entries.map(e, e.label).all(l, l != 'b')", "unknown", possible},
		{"self.entries.map(e, e.label).all(l, l != 'a')", "false", possible},
		{"self.entries.map(e, e.label != 'b', e.label).all(l, l != 'a')", "false", possible},
		{"self.entries.map(e, e.label != 'a', e.label).all(l, l != 'a')", "true", nil},
		{"self.entries.map(e, e.label != 'a', e.label).all(l, l != 'b')", "unknown", possible},
		// + keeps each item's status
		{"(self.entries + self.more).exists(e, e.label == 'd')", "true", possible},
		{"(self.more + self.entries).exists(e, e.label == 'b')", "unknown", possible},
		{"(self.more + self.entries).all(e, e.label != 'b')", "unknown", possible},
		{"(self.more + self.more).exists(e, e.label == 'd')", "true", possible},
		{"size(self.more + self.entries) == 4", "unknown", possible},
		{"size(self.more + self.more) == 2", "true", possible},
		// every other function, macro and operator over such a list is unknown
		{"size(self.entries) == 3", "unknown", possible},
		{"self.entries.size() == 3", "unknown", possible},
		{"self.entries[0].label == 'a'", "unknown", possible},
		{"self.entries[2].label == 'c'", "unknown", possible},
		{"size(self.entries.slice(0, 1)) == 1", "unknown", possible},
		{"size(self.entries.take(1)) == 1", "unknown", possible},
		{"size(self.entries.distinct()) == 3", "unknown", possible},
		{"size(self.entries.map(e, [e]).flatten()) == 3", "unknown", possible},
		{"self.entries.map(e, e.label).sortBy(l, l) == ['a', 'b', 'c']", "unknown", possible},
		{"self.entries == self.entries", "unknown", possible},
		{"self.entries != self.more", "unknown", possible},
		{"self.more == self.entries", "unknown", possible},
		{"self.more != self.entries", "unknown", possible},
		{"self.entries == self.more || true", "true", possible},
		// a list or a map that holds such a list, at any depth, is compared like one
		{"[self.entries] == [self.entries]", "unknown", possible},
		{"[self.entries] == [self.more]", "unknown", possible},
		{"[self.entries] != [self.more]", "unknown", possible},
		{"{'k': self.entries} == {'k': self.more}", "unknown", possible},
		{"self.entries in [self.entries]", "unknown", possible},
		{"size([self.entries, self.more].distinct()) == 2", "unknown", possible},
		{"[self.entries] == [self.more] || true", "true", possible},
		{"[self.entries].exists(l, l.exists(e, e.label == 'a'))", "true", possible},
		{"[self.entries].exists(l, l.exists(e, e.label == 'b'))", "unknown", possible},
		// a plain list reads as before
		{"self.entries.exists(e, e.label == 'b')", "true", nil},
		{"size(self.entries) == 3 && self.entries == self.entries", "true", nil},
		{"self.entries.all(e, e.label != 'z') && !self.entries.exists(e, e.label == 'z')", "true", nil},
		{"size(self.entries.filter(e, e.label != 'b')) == 2", "true", nil},
		{"(self.more + self.entries).exists(e, e.label == 'c')", "true", nil},
		{"self.tags.all(k, k == 'k') && self.tags.exists(k, k == 'k') && !self.tags.exists(k, k == 'z')", "true", nil},
		{"'k' in self.tags", "true", nil},
		// a macro over a map still builds no list
	} {
		got, problems := u.EvaluateCheckWith("demo", "value", nil, tc.expr, self, tc.possible)
		if len(problems) > 0 {
			t.Errorf("%s: %v", tc.expr, problems)
			continue
		}
		if got.Value != tc.want {
			t.Errorf("%s (possible %v): got %s (%s), want %s", tc.expr, tc.possible, got.Value, got.Reason, tc.want)
		}
	}
	for _, expr := range []string{
		"size(self.tags.map(k, k)) == 1",
		"size(self.tags.filter(k, true)) == 1",
		"self.entries.exists_one(e, true)",
	} {
		if _, problems := u.EvaluateCheck("demo", "value", nil, expr, self); len(problems) == 0 {
			t.Errorf("%s: accepted", expr)
		}
	}
	// A vector that marks an item that is not there is an error.
	if _, problems := u.EvaluateCheckWith("demo", "value", nil, "true", self, map[string][]int{"entries": {3}}); len(problems) == 0 {
		t.Errorf("an item past the end of the list was marked possible")
	}
	if _, problems := u.EvaluateCheckWith("demo", "value", nil, "true", self, map[string][]int{"tags": {0}}); len(problems) == 0 {
		t.Errorf("a field that is not a list was marked possible")
	}
}

// An unknown that met an error and one that met none are read apart: a possible member whose
// predicate or mapping met an error makes the result unknown, while one that is unknown only
// because what it reads waits on a possible member stays a possible member. What an evaluation
// meets does not depend on the order of the operands of && and ||.
func TestPossibleMembersMeetErrors(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  value:\n    purpose: x\n    types:\n      entry:\n        fields:\n          label: { type: string }\n    structure:\n      entries: { type: list(entry) }\n      words: { type: list(string) }\n"
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u, problems := Prepare(map[string]*Package{"demo": Build("demo", "demo.yaml", content)})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	entry := func(label string) any { return map[string]any{"label": label} }
	self := map[string]any{
		"entries": []any{entry("a"), entry("b"), entry("c")},
		"words":   []any{"a", "b", "c"},
	}
	possible := map[string][]int{"entries": {1}}
	for _, tc := range []struct {
		expr, want string
		possible   map[string][]int
	}{
		// filter: a possible member whose predicate met an error makes the result unknown
		{"self.entries.filter(e, e.label != 'b' || self.words[7] == 'x').exists(e, e.label == 'a')", "unknown", possible},
		// and one whose predicate waits on a possible member stays a possible member
		{"self.entries.filter(e, e.label != 'b' || size(self.entries) == 3).exists(e, e.label == 'a')", "true", possible},
		{"size(self.entries.filter(e, e.label != 'b' || size(self.entries) == 3)) == 3", "unknown", possible},
		// an error met beside an unknown that met none counts, whichever operand comes first
		{"self.entries.filter(e, e.label != 'b' || size(self.entries) == 3 || self.words[7] == 'x').exists(e, e.label == 'a')", "unknown", possible},
		{"self.entries.filter(e, e.label != 'b' || self.words[7] == 'x' || size(self.entries) == 3).exists(e, e.label == 'a')", "unknown", possible},
		{"self.entries.filter(e, e.label == 'a' || e.label == 'b' && size(self.entries) == 3 && self.words[7] == 'x').exists(e, e.label == 'a')", "unknown", possible},
		{"self.entries.filter(e, e.label == 'a' || e.label == 'b' && self.words[7] == 'x' && size(self.entries) == 3).exists(e, e.label == 'a')", "unknown", possible},
		// a side that decides still decides
		{"self.entries.filter(e, e.label != 'b' || self.words[7] == 'x' || true).exists(e, e.label == 'a')", "true", possible},
		{"self.entries.filter(e, e.label == 'a' || (self.words[7] == 'x' && false)).exists(e, e.label == 'a')", "true", possible},
		// for a certain member any unknown predicate makes the result unknown, as before
		{"self.entries.filter(e, e.label != 'b' || self.words[7] == 'x').exists(e, e.label == 'a')", "unknown", nil},
		// map: a possible member whose mapping waits stays, with a value not yet known
		{"self.entries.map(e, e.label == 'b' ? self.entries[0].label : e.label).exists(l, l == 'a')", "true", possible},
		{"self.entries.map(e, e.label == 'b' ? self.entries[0].label : e.label).exists(l, l == 'z')", "unknown", possible},
		{"self.entries.map(e, e.label == 'b' ? self.entries[0].label : e.label).all(l, l != 'c')", "false", possible},
		{"'a' in self.entries.map(e, e.label == 'b' ? self.entries[0].label : e.label)", "true", possible},
		{"'z' in self.entries.map(e, e.label == 'b' ? self.entries[0].label : e.label)", "unknown", possible},
		// a possible member whose mapping met an error, or a certain member whose mapping is unknown, makes it unknown
		{"self.entries.map(e, e.label == 'b' ? self.words[7] : e.label).exists(l, l == 'a')", "unknown", possible},
		{"self.entries.map(e, e.label == 'a' ? self.entries[0].label : e.label).exists(l, l == 'c')", "unknown", possible},
		// exists is false only when the predicate is false for every item, possible members included
		{"self.entries.exists(e, e.label == 'b' ? size(self.entries) == 0 : false)", "unknown", possible},
		{"self.entries.exists(e, e.label == 'b' ? self.words[7] == 'x' : false)", "unknown", possible},
		{"self.entries.exists(e, e.label == 'z')", "false", possible},
		// && and || keep their absorption with both operands evaluated
		{"false && self.words[7] == 'x'", "false", nil},
		{"self.words[7] == 'x' || true", "true", nil},
		{"self.words[7] == 'x' && true", "unknown", nil},
		{"true && true", "true", nil},
		{"false || false", "false", nil},
	} {
		got, problems := u.EvaluateCheckWith("demo", "value", nil, tc.expr, self, tc.possible)
		if len(problems) > 0 {
			t.Errorf("%s: %v", tc.expr, problems)
			continue
		}
		if got.Value != tc.want {
			t.Errorf("%s (possible %v): got %s (%s), want %s", tc.expr, tc.possible, got.Value, got.Reason, tc.want)
		}
	}
}

// A macro reads its receiver once, so a long chain of macros expands in proportion to its length
// and is evaluated at once.
func TestChainedMacrosExpandLinearly(t *testing.T) {
	src := "format: 1\npackage: demo\nrevision: 1\ndefinitions:\n  value:\n    purpose: x\n    types:\n      entry:\n        fields:\n          label: { type: string }\n    structure:\n      entries: { type: list(entry) }\n"
	content, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u, problems := Prepare(map[string]*Package{"demo": Build("demo", "demo.yaml", content)})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	self := map[string]any{"entries": []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}}}
	for _, n := range []int{12, 40} {
		expr := "self.entries"
		for i := 0; i < n; i++ {
			expr += ".filter(e, e.label != 'z')"
		}
		expr += ".exists(e, e.label == 'a')"
		got, problems := u.EvaluateCheck("demo", "value", nil, expr, self)
		if len(problems) > 0 {
			t.Errorf("a chain of %d filters: %v", n, problems)
			continue
		}
		if got.Value != "true" {
			t.Errorf("a chain of %d filters: got %s (%s), want true", n, got.Value, got.Reason)
		}
	}
}
