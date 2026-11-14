package verify

import (
	"io/fs"
	"os"
	"path"
	"testing"
	"testing/fstest"

	"github.com/altakleos/strux/format"
)

// vectorCase is one case of a verification vector: the inputs of verify, and the fields of the
// result that a conforming verifier returns for them.
type vectorCase struct {
	vector string
	name   string
	inputs map[string]any
	want   map[string]any
}

// vectors reads every verification vector in testdata/vectors: each a log in the interchange
// layout, with its cases beside it in cases.json.
func vectors(t *testing.T) map[string]*Log {
	t.Helper()
	root := os.DirFS("testdata/vectors")
	dirs, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Log{}
	for _, d := range dirs {
		sub, err := fs.Sub(root, d.Name())
		if err != nil {
			t.Fatal(err)
		}
		l, err := ReadLog(sub)
		if err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		out[d.Name()] = l
	}
	if len(out) == 0 {
		t.Fatal("testdata/vectors holds no vector")
	}
	return out
}

func cases(t *testing.T, vector string) []vectorCase {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS("testdata/vectors"), path.Join(vector, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := format.ParseJSON(data)
	if err != nil {
		t.Fatalf("%s: cases.json: %v", vector, err)
	}
	var out []vectorCase
	for _, c := range v.(map[string]any)["cases"].([]any) {
		m := c.(map[string]any)
		out = append(out, vectorCase{vector: vector, name: m["case"].(string), inputs: m["inputs"].(map[string]any), want: m["want"].(map[string]any)})
	}
	return out
}

// The first step reads each entry: a fact, or an invalid entry with what the encoding refuses of
// it. Every case's invalid entries are the ones the vector states.
func TestEntriesAreTheFactsAndInvalidEntriesTheVectorsState(t *testing.T) {
	for name, l := range vectors(t) {
		for _, c := range cases(t, name) {
			read := c.inputs["log"].(map[string]any)
			n := int(read["size"].(int64))
			entries := l.ReadEntries(read["log"].(string), n)
			if len(entries) != n {
				t.Errorf("%s/%s: read %d entries, want %d", name, c.name, len(entries), n)
			}
			stated, ok := c.want["invalid"].([]any)
			if !ok {
				continue
			}
			want := map[int64]string{}
			for _, item := range stated {
				m := item.(map[string]any)
				want[m["position"].(int64)] = m["refused"].(string)
			}
			for _, e := range entries {
				if got, stated := e.Refused, want[int64(e.Position)]; got != stated {
					t.Errorf("%s/%s: the entry at %d is refused for %q, and the vector states %q", name, c.name, e.Position, got, stated)
				}
			}
		}
	}
}

// A log without anchors or blobs is still a log, and a record the encoding cannot read is an
// invalid entry, refused as a record.
func TestALogNeedsOnlyItsRecords(t *testing.T) {
	l, err := ReadLog(fstest.MapFS{"records.jsonl": {Data: []byte("not a record\n")}})
	if err != nil {
		t.Fatal(err)
	}
	entries := l.ReadEntries("sha256:00", 5)
	if len(entries) != 1 || entries[0].Fact != nil || entries[0].Refused != format.RefusedRecord {
		t.Errorf("got %+v, want one entry refused as a record", entries)
	}
}
