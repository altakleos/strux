package format

import (
	"strings"
	"testing"
)

// notes is a package whose use reads the log through every function a Reader answers.
const notes = `format: 1
package: notes
revision: 1
definitions:
  note:
    purpose: A note that may be about another note.
    structure:
      text: { type: text, required: true }
      about: { type: ref(note) }
      due: { type: time }
    uses:
      recognize: {}
      read:
        beneficiaries: "[]"
        let:
          others: "history(notes.note)"
        requires:
          about-says-something: { rule: x, expr: "!has(self.about) || size(self.about.content().text) > 0" }
          about-by-alice: { rule: x, expr: "!has(self.about) || self.about.fact().signer == 'alice'" }
          about-digest: { rule: x, expr: "!has(self.about) || self.about.digest() == 'sha256:aa'" }
          about-in-force: { rule: x, expr: "!has(self.about) || in_force(self.about)" }
          is-alone: { rule: x, expr: "others.size() == 1" }
          some-other-is-the-about: { rule: x, expr: "!has(self.about) || others.exists(o, o == self.about)" }
          due-later: { rule: x, expr: "fact.append_by != self.due" }
          signed-by-bob: { rule: x, expr: "fact.signer == 'bob'" }
      weigh:
        beneficiaries: "[]"
        requires:
          about-recognized: { rule: x, expr: "has(self.about) && recognized(self.about)" }
          about-current: { rule: x, expr: "has(self.about) && current(self.about).digest() == 'sha256:bb'" }
          bob-may-read: { rule: x, expr: "authorized('bob', 'notes/note.uses.read')" }
          bob-apart: { rule: x, expr: "outside_reach('bob', ['alice', 'carol'])" }
          about-holds: { rule: x, expr: "has(self.about) && holds(self.about, 'read')" }
          about-permitted: { rule: x, expr: "has(self.about) && permitted(self.about, 'read')" }
          retains: { rule: x, expr: "retained('sha256:cc')" }
          quotes: { rule: x, expr: "quoted('sha256:cc', 'L1', 'hello')" }
`

type fakeReader struct {
	history  []Reference
	possible []int
	inForce  string
	answers  map[string]string // what each function that answers true, false or unknown returns
	current  Reference
	calls    *[]string // each call of those functions, with its arguments
}

func (f fakeReader) answer(call string) Result {
	if f.calls != nil {
		*f.calls = append(*f.calls, call)
	}
	name, _, _ := strings.Cut(call, "(")
	return Result{Value: f.answers[name], Reason: name + " is not decided"}
}

func (f fakeReader) Recognized(r Reference) Result { return f.answer("recognized(" + r.Fact + ")") }

func (f fakeReader) Current(r Reference) (Reference, Result) {
	return f.current, f.answer("current(" + r.Fact + ")")
}

func (f fakeReader) Authorized(principal, use string) Result {
	return f.answer("authorized(" + principal + ", " + use + ")")
}

func (f fakeReader) OutsideReach(principal string, principals []string) Result {
	return f.answer("outside_reach(" + principal + ", " + strings.Join(principals, " ") + ")")
}

func (f fakeReader) Holds(r Reference, use string) Result {
	return f.answer("holds(" + r.Fact + ", " + use + ")")
}

func (f fakeReader) Permitted(r Reference, use string) Result {
	return f.answer("permitted(" + r.Fact + ", " + use + ")")
}

func (f fakeReader) Findings(check, digest string, candidate bool) ([]Reference, []int) {
	return nil, nil
}

func (f fakeReader) Retained(digest string) Result { return f.answer("retained(" + digest + ")") }

func (f fakeReader) Quoted(digest, location, quote string) Result {
	return f.answer("quoted(" + digest + ", " + location + ", " + quote + ")")
}

func (f fakeReader) Resolve(r Reference) (string, map[string]any, map[string]any, bool) {
	if r.Fact != "sha256:f1" {
		return "", nil, nil, false
	}
	return "notes/note", map[string]any{"text": "hello"}, map[string]any{"signer": "alice", "position": int64(3)}, true
}

func (f fakeReader) History(definition string) ([]Reference, []int) {
	if definition != "notes/note" {
		return nil, nil
	}
	return f.history, f.possible
}

func (f fakeReader) InForce(Reference) Result { return Result{Value: f.inForce, Reason: "not yet"} }

func notesUniverse(t *testing.T) *Universe {
	t.Helper()
	content, err := Parse([]byte(notes))
	if err != nil {
		t.Fatal(err)
	}
	u, problems := Prepare(map[string]*Package{"notes": Build("notes", "notes.yaml", content)})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	return u
}

func TestAUseReadsTheLogThroughItsReader(t *testing.T) {
	u := notesUniverse(t)
	about := Reference{Fact: "sha256:f1", Digest: "sha256:aa"}
	in := UseInputs{
		Self: map[string]any{"text": "a note", "about": map[string]any{"fact": about.Fact, "digest": about.Digest}, "due": "2026-02-01T00:00:00Z"},
		Fact: map[string]any{"signer": "bob", "append_by": "2026-01-01T00:00:00Z"},
	}
	got, err := u.EvaluateUse("notes", "note", "read", in, fakeReader{history: []Reference{about}, inForce: "true"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"about-says-something", "about-by-alice", "about-digest", "about-in-force", "is-alone", "some-other-is-the-about", "due-later", "signed-by-bob"} {
		if got[name].Value != "true" {
			t.Errorf("%s: got %+v, want true", name, got[name])
		}
	}
	// A reference that resolves to nothing recognized leaves what reads its content unknown, and
	// its digest is still the one it holds.
	in.Self["about"] = map[string]any{"fact": "sha256:f2", "digest": "sha256:aa"}
	got, err = u.EvaluateUse("notes", "note", "read", in, fakeReader{history: []Reference{about}, possible: []int{0}, inForce: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"about-says-something": "unknown", "about-by-alice": "unknown", "about-digest": "true", "about-in-force": "unknown", "is-alone": "unknown", "due-later": "true"} {
		if got[name].Value != want {
			t.Errorf("unresolved, %s: got %+v, want %s", name, got[name], want)
		}
	}
	// A condition of the use that does not type-check is reported, and nothing is evaluated.
	if _, err := u.EvaluateUse("notes", "note", "absent", in, fakeReader{}); err == nil || !strings.Contains(err.Error(), "declares no use absent") {
		t.Errorf("a use the definition lacks: got %v", err)
	}
}

// Each function that reads the log beyond a reference and the history is answered by the reader,
// with the arguments the expression gives it, and an answer it cannot give leaves the condition
// unknown.
func TestTheLogReadingFunctionsAskTheReader(t *testing.T) {
	u := notesUniverse(t)
	about := Reference{Fact: "sha256:f1", Digest: "sha256:aa"}
	in := UseInputs{Self: map[string]any{"text": "a note", "about": map[string]any{"fact": about.Fact, "digest": about.Digest}}, Fact: map[string]any{"signer": "bob"}}
	all := func(v string) map[string]string {
		out := map[string]string{}
		for _, f := range []string{"recognized", "current", "authorized", "outside_reach", "holds", "permitted", "retained", "quoted"} {
			out[f] = v
		}
		return out
	}
	var calls []string
	got, err := u.EvaluateUse("notes", "note", "weigh", in, fakeReader{answers: all("true"), current: Reference{Fact: "sha256:f9", Digest: "sha256:bb"}, calls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	for name, res := range got {
		if res.Value != "true" {
			t.Errorf("%s: got %+v, want true", name, res)
		}
	}
	for _, want := range []string{"recognized(sha256:f1)", "current(sha256:f1)", "authorized(bob, notes/note.uses.read)", "outside_reach(bob, alice carol)", "holds(sha256:f1, read)", "permitted(sha256:f1, read)", "retained(sha256:cc)", "quoted(sha256:cc, L1, hello)"} {
		found := false
		for _, c := range calls {
			found = found || c == want
		}
		if !found {
			t.Errorf("the reader was not asked %s; it was asked %v", want, calls)
		}
	}
	for _, v := range []string{"false", "unknown"} {
		got, err := u.EvaluateUse("notes", "note", "weigh", in, fakeReader{answers: all(v)})
		if err != nil {
			t.Fatal(err)
		}
		for name, res := range got {
			if want := map[bool]string{true: "unknown", false: v}[name == "about-current"]; res.Value != want {
				t.Errorf("answered %s, %s: got %+v, want %s", v, name, res, want)
			}
		}
	}
}
