package format

import (
	"strings"
	"testing"
)

// shaped is a package whose definition holds one field of each kind a structure can have, and
// whose operation types what it reads and what it returns.
const shaped = partsHead + `types:
  size: { enum: [small, large] }
definitions:
  thing:
    purpose: A thing with one field of each kind.
    types:
      part:
        fields:
          name: { type: string, required: true }
          count: { type: integer }
      choice:
        one_of:
          word: { type: string }
          number: { type: integer }
    structure:
      title: { type: string, required: true, max: 8 }
      body: { type: text }
      count: { type: integer }
      flag: { type: boolean }
      digest: { type: digest }
      when: { type: time }
      at: { type: position }
      path: { type: name }
      line: { type: location }
      who: { type: principal }
      size: { type: size }
      grade: { type: "enum(a, b)" }
      parts: { type: list(part), key: name, max: 2 }
      tags: { type: "map(string, integer)" }
      choice: { type: choice }
      other: { type: ref(thing) }
      rule: { type: combination }
      later: { type: string, enabled: false }
    operations:
      look:
        at: verification
        inputs:
          depth: { type: integer, required: true }
        outputs:
          found: { type: list(position), required: true }
          part: { type: part }
        returns: What it found.
    uses:
      recognize: {}
`

func shapedThing(t *testing.T) *Definition {
	t.Helper()
	p := buildSource(t, shaped)
	if _, problems := Prepare(map[string]*Package{"demo": p}); len(problems) > 0 {
		t.Fatal(problems)
	}
	return p.Defs["thing"]
}

const aDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// An instance conforms to its structure when every required field is set, no field is unknown,
// each value has the type of its field, no string or list is above its size, and no two items of
// a list share its key.
func TestConforms(t *testing.T) {
	thing := shapedThing(t).Decl
	whole := map[string]any{
		"title": "a thing", "body": "Some prose.", "count": int64(-3), "flag": false, "digest": aDigest,
		"when": "2026-01-10T09:00:00Z", "at": int64(0), "path": "a/b", "line": "L5-L9", "who": "owner",
		"size": "large", "grade": "b", "tags": map[string]any{"x": int64(1)},
		"parts":  []any{map[string]any{"name": "one"}, map[string]any{"name": "two", "count": int64(2)}},
		"choice": map[string]any{"number": int64(4)},
		"other":  map[string]any{"fact": aDigest, "digest": aDigest},
		"rule":   map[string]any{"rule": "unanimous", "cap": int64(3)},
		"later":  "a field this revision does not interpret is still a field",
	}
	if problems := Conforms(thing, whole); len(problems) > 0 {
		t.Fatalf("an instance that sets every field conforms: %v", problems)
	}
	if problems := Conforms(thing, map[string]any{"title": "least"}); len(problems) > 0 {
		t.Fatalf("an instance that sets the required field alone conforms: %v", problems)
	}
	for name, tc := range map[string]struct {
		field string
		value any
		want  string
	}{
		"a missing required field":               {"title", nil, "title: is required"},
		"an unknown field":                       {"extra", "x", "extra: demo/thing has no such field"},
		"an empty string":                        {"title", "", "title: a string is never empty"},
		"a string above its size":                {"title", "more than eight", "title: is 15 bytes, more than 8"},
		"an integer for a string":                {"title", int64(1), "title: must be a string"},
		"a string for an integer":                {"count", "three", "count: must be an integer"},
		"an integer outside the data":            {"count", int64(1) << 53, "count: must be an integer"},
		"a word for a boolean":                   {"flag", "yes", "flag: must be true or false"},
		"a digest in capitals":                   {"digest", strings.ToUpper(aDigest), "digest: must be a digest"},
		"a time with an offset":                  {"when", "2026-01-10T09:00:00+00:00", "when: must be a time"},
		"a negative position":                    {"at", int64(-1), "at: a position is not negative"},
		"a name with an empty part":              {"path", "a//b", "path: must be non-empty segments"},
		"a location without a line":              {"line", "5", "line: must be a line or a line range"},
		"an empty principal":                     {"who", "", "who: a principal is never empty"},
		"a value outside a declared enumeration": {"size", "medium", "size: must be one of small, large"},
		"a value outside an inline enumeration":  {"grade", "c", "grade: must be one of a, b"},
		"a list above its size": {"parts", []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}, map[string]any{"name": "c"}},
			"parts: holds 3 items, more than 2"},
		"two items that share a key": {"parts", []any{map[string]any{"name": "a"}, map[string]any{"name": "a"}},
			"parts[1]: shares its name, a, with an earlier item"},
		"an item without its required field": {"parts", []any{map[string]any{"count": int64(1)}}, "parts[0].name: is required"},
		"a map value of the wrong type":      {"tags", map[string]any{"x": "one"}, "tags[x]: must be an integer"},
		"a variant with two alternatives": {"choice", map[string]any{"word": "w", "number": int64(1)},
			"choice: a value of demo/thing.choice sets exactly one of its alternatives, and this sets 2"},
		"a variant with none":               {"choice", map[string]any{}, "sets exactly one of its alternatives, and this sets 0"},
		"a reference without its digest":    {"other", map[string]any{"fact": aDigest}, "other: must be a reference"},
		"a built-in record without a field": {"rule", map[string]any{"rule": "unanimous"}, "rule.cap: is required"},
	} {
		t.Run(name, func(t *testing.T) {
			instance := map[string]any{"title": "a thing"}
			if tc.value == nil {
				delete(instance, tc.field)
			} else {
				instance[tc.field] = tc.value
			}
			problems := Conforms(thing, instance)
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Errorf("got %q, want one problem that says %q", problems, tc.want)
			}
		})
	}
	if problems := Conforms(thing, "a string"); len(problems) != 1 || !strings.Contains(problems[0], "must be a mapping of the fields of demo/thing") {
		t.Errorf("a value that is no mapping: got %q", problems)
	}
}

// A reference is held to its form alone unless the caller says what it names: only a caller that
// knows the facts can tell, and it is given the definitions the field admits.
func TestConformsToChecksWhatAReferenceNames(t *testing.T) {
	thing := shapedThing(t).Decl
	instance := map[string]any{"title": "a", "other": map[string]any{"fact": aDigest, "digest": aDigest}}
	if problems := Conforms(thing, instance); len(problems) != 0 {
		t.Fatalf("a reference in its form: %q", problems)
	}
	var admitted []string
	refer := func(reference map[string]any, admits []*Decl) string {
		for _, d := range admits {
			admitted = append(admitted, d.Path())
		}
		return "names a fact of another kind"
	}
	problems := ConformsTo(thing, instance, refer)
	if len(problems) != 1 || problems[0] != "other: names a fact of another kind" {
		t.Errorf("got %q, want the caller's one problem at the field", problems)
	}
	if len(admitted) != 1 || admitted[0] != "demo/thing" {
		t.Errorf("the caller was given %q, want the definition the field admits", admitted)
	}
	// A reference that is not in its form is reported once, and the caller is not asked.
	instance["other"] = "a string"
	admitted = nil
	if problems := ConformsTo(thing, instance, refer); len(problems) != 1 || admitted != nil {
		t.Errorf("a reference out of its form: got %q, and the caller was asked about %q", problems, admitted)
	}
}

// An operation types what it returns with outputs, as it types what it reads with inputs, and a
// value that states only some of the outputs conforms partly: what it states is checked in full,
// and what it leaves out is not asked for.
func TestAnOperationTypesItsOutputs(t *testing.T) {
	thing := shapedThing(t)
	in, out := thing.Inputs["look"], thing.Outputs["look"]
	if in == nil || out == nil {
		t.Fatalf("the operation has inputs %v and outputs %v, want both", in, out)
	}
	if problems := Conforms(in, map[string]any{"depth": int64(2)}); len(problems) > 0 {
		t.Errorf("the inputs: %v", problems)
	}
	if problems := Conforms(out, map[string]any{"part": map[string]any{"name": "one"}}); len(problems) != 1 || !strings.Contains(problems[0], "found: is required") {
		t.Errorf("outputs that leave out a required field: got %q", problems)
	}
	if problems := ConformsPartly(out, map[string]any{"part": map[string]any{"name": "one"}}); len(problems) > 0 {
		t.Errorf("a result that states one output conforms partly: %v", problems)
	}
	if problems := ConformsPartly(out, map[string]any{"part": map[string]any{"count": int64(1)}}); len(problems) != 1 || !strings.Contains(problems[0], "part.name: is required") {
		t.Errorf("a stated output is checked in full: got %q", problems)
	}
	if problems := ConformsPartly(out, map[string]any{"found": []any{int64(1), "two"}}); len(problems) != 1 || !strings.Contains(problems[0], "found[1]: must be an integer") {
		t.Errorf("a stated list is checked item by item: got %q", problems)
	}
	// An output is a field specification, so its type must resolve like any other.
	broken := buildSource(t, strings.Replace(shaped, "found: { type: list(position), required: true }", "found: { type: list(nothing), required: true }", 1))
	_, problems := Prepare(map[string]*Package{"demo": broken})
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "thing.operations.look.outputs.found") {
		t.Errorf("an output of an unknown type: got %q, want a problem at the output", problems)
	}
}
