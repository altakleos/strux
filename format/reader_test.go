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
`

type fakeReader struct {
	history  []Reference
	possible []int
	inForce  string
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
